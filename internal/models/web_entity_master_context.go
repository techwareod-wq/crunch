package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/utils"
)

const webEntityMasterContextCollection = "webEntityMasterContext"

// Image positions used on CGEImage.Position and as ImageGenPrompts map keys.
const (
	ImagePositionThumbnail  = "thumbnail"
	ImagePositionMidArticle = "mid-article"
)

// Placeholder tokens the pipeline writes into article content before image
// generation; replaced with real image markdown by the image-replacement step.
const (
	PlaceholderImageThumbnail  = "{{IMAGE_THUMBNAIL}}"
	PlaceholderImageMidArticle = "{{IMAGE_MID_ARTICLE}}"
)

// Tavily topic-research variants dispatched by the CGE pipeline.
const (
	TavilyVariantNews     = "news"
	TavilyVariantExpert   = "expert"
	TavilyVariantMistakes = "mistakes"
)

// RegeneratedImageKeyPrefix is the S3 key namespace for user-triggered image
// regenerations. Regens never overwrite the pipeline's deterministic
// generated_images key — each regen writes a fresh versioned object under this
// prefix, the client previews it, and save-draft commits it (deleting the
// replaced object) exactly like a user-uploaded replacement. Shared between
// the CGE writer and the save-draft key validator so the two can't drift.
func RegeneratedImageKeyPrefix(userID, masterContextID string) string {
	return fmt.Sprintf("generated_images/regen/%s-%s", userID, masterContextID)
}

// EnsureWebEntityMasterContextIndexes creates the scheduled_article_id index.
// Every access to this collection that isn't by _id keys off that field —
// GetMasterContextsByScheduledArticleIDs ($in, run by every keyword read path to
// surface the wire-only "error" status), GetWebEntityMasterContextByScheduledArticleID,
// and the delete path. All of them collection-scanned before this existed, and
// the keyword-search endpoint runs the $in per keystroke. Not unique: a slot's
// master context is deleted and regenerated on retry, so transient duplicates
// must not fail the write. Idempotent.
func EnsureWebEntityMasterContextIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		// Kept anonymous so the auto-name matches the index existing
		// deployments already carry (scheduled_article_id_1) — an explicit
		// name would IndexOptionsConflict on redeploy.
		{Keys: bson.D{{Key: "scheduled_article_id", Value: 1}}},
		// Partial index backing the GSC analytics article-stamping lookup
		// (FindPublishedNormalizedURLIndex): only docs with a stamped
		// publish.normalized_url participate.
		{
			Keys: bson.D{{Key: "publish.normalized_url", Value: 1}},
			Options: options.Index().
				SetName("publish_normalized_url_partial").
				SetPartialFilterExpression(bson.M{"publish.normalized_url": bson.M{"$exists": true, "$type": "string", "$gt": ""}}),
		},
	}
	if _, err := Collection(webEntityMasterContextCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure web entity master context indexes: %w", err)
	}
	return nil
}

const (
	CGEStatusCreated        = 0
	CGEStatusProcessing     = 1
	CGEOutlineGenerated     = 2
	CGEStatusError          = 3
	CGEStatusDone           = 4
	CGEStatusReadyForReview = 5
)

type WebEntityMasterContext struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty"`
	UserID             primitive.ObjectID `bson:"user_id"`
	WebEntityContextID primitive.ObjectID `bson:"web_entity_context_id"`
	// ScheduledArticleID is the calendar slot this generated article belongs
	// to. Populated when CGE Orchestrate is dispatched from an SE-produced
	// SQS message; the article endpoint uses it to look up generated content
	// for a scheduled slot.
	ScheduledArticleID primitive.ObjectID `bson:"scheduled_article_id,omitempty"`

	// KeywordID references the keyword doc in the `keyword` collection. All
	// triggering keyword data (volume, kd, cpc, intent, funnel, cluster id,
	// opportunity score, pillar flag, the keyword string itself) is read
	// from that doc — no denormalised copy lives here.
	KeywordID primitive.ObjectID `bson:"keyword_id"`

	// Per-article scheduling metadata produced by the Scheduling Engine.
	ArticleType   string `bson:"article_type"`
	ProposedTitle string `bson:"proposed_title"`
	// AdditionalInstructions is the user-supplied guidance copied from the
	// ScheduledArticle at orchestrate time. Threaded into the article
	// generation prompt; empty when the user hasn't set anything.
	AdditionalInstructions string         `bson:"additional_instructions,omitempty"`
	ScheduledDate          string         `bson:"scheduled_date,omitempty"`
	Sources                []string       `bson:"sources,omitempty"`
	UserRankingPosition    int            `bson:"user_ranking_position"`
	UserRankingURL         string         `bson:"user_ranking_url,omitempty"`
	CompetitorRankings     map[string]int `bson:"competitor_rankings,omitempty"`

	WebsiteURL      string           `bson:"website_url"`
	BusinessContext *BusinessContext `bson:"business_context,omitempty"`
	Competitors     []Competitor     `bson:"competitors,omitempty"`
	CountryCode     string           `bson:"country_code"`
	LocationCode    int              `bson:"location_code,omitempty"`
	UserDR          int              `bson:"user_dr"`

	SecondaryKeywords []string `bson:"secondary_keywords,omitempty"`

	// InternalLinkingEnabled controls whether the internal-link-insertion
	// pipeline step runs after article generation. Resolved at create time
	// from the triggering ScheduledArticle's per-article toggle, falling back
	// to the WebEntity default. Stored without omitempty so an explicit `false`
	// persists. See [[Internal Link Insertion]].
	InternalLinkingEnabled bool `bson:"internal_linking_enabled"`

	// ThumbnailStyle is the resolved thumbnail-style ID stamped at create time
	// (per-article override, else WebEntity default, else system default). The
	// image-generation step reads it from here and never touches the
	// ScheduledArticle / WebEntity. Empty on master contexts created before this
	// field existed → the read-time lookup falls back to the default template.
	// Since style replication: ALSO empty when the learned image style won the
	// resolution (no per-article pick + ImageStylePrompt set) — the two fields
	// together encode the ladder: explicit pick → learned → entity default.
	// See [[Thumbnail Style Selection]] and [[Style Replication]].
	ThumbnailStyle string `bson:"thumbnail_style,omitempty"`

	// Learned style artifacts (style replication) snapshotted at create time —
	// master-context semantics: in-flight articles keep the style they started
	// with, new generations pick up later changes. Empty = no learned profile
	// (or the artifact wasn't learned); every consumer's template block is
	// conditional. ToneProfile feeds article/section/meta prompts (+ a light
	// heading-voice hint in outline gen), StructurePattern feeds outline gen,
	// and the two per-position image style prompts feed thumbnail /
	// mid-article image gen and image regen independently. (The learned title
	// pattern is NOT snapshotted — titles are produced by the scheduling
	// engine before a master context exists, off the WebEntity directly.)
	ToneProfile           string `bson:"tone_profile,omitempty"`
	StructurePattern      string `bson:"structure_pattern,omitempty"`
	ThumbnailStylePrompt  string `bson:"thumbnail_style_prompt,omitempty"`
	MidArticleStylePrompt string `bson:"mid_article_style_prompt,omitempty"`

	SerpData        *CGESerpData         `bson:"serp_data,omitempty"`
	TopicResearch   *CGETopicResearch    `bson:"topic_research,omitempty"`
	YouTubeInsights *CGEYouTubeInsights  `bson:"youtube_insights,omitempty"`
	YouTubeVideos   []YouTubeVideoStatus `bson:"youtube_videos,omitempty"`
	SerpGapAnalysis *CGESerpGapAnalysis  `bson:"serp_gap_analysis,omitempty"`
	Outline         *CGEOutline          `bson:"outline,omitempty"`
	ArticleContent  string               `bson:"article_content,omitempty"`

	// WordCount is the count of words in the currently-saved article content
	// (image markdown and image placeholders excluded). Recomputed whenever
	// the article body is written — by the pipeline (article generation,
	// image replacement) and by user draft saves.
	WordCount int `bson:"word_count"`

	Images              []CGEImage       `bson:"images,omitempty"`
	SchemaMarkup        *CGESchemaMarkup `bson:"schema_markup,omitempty"`
	MetaAssets          *CGEMetaAssets   `bson:"meta_assets,omitempty"`
	FinalArticleContent string           `bson:"final_article_content,omitempty"`

	// User edits made from the review screen. Non-nil iff the user has saved
	// at least one draft; takes precedence over the pipeline-produced
	// ArticleContent / MetaAssets / url slug in the read path. The
	// scheduled-article status is flipped to `draft` whenever this is set.
	Edited *CGEEdited `bson:"edited,omitempty"`

	// PendingRegen holds un-committed regenerations awaiting the user's
	// save-or-undo decision. Recorded by the regenerate endpoints, surfaced on
	// the article read so any device can restore them, removed per-entry by
	// the discard endpoint, and cleared wholesale when a draft save commits.
	// The article body/images themselves are NEVER mutated by a regen — these
	// entries are the only server-side trace until the user decides.
	PendingRegen *CGEPendingRegen `bson:"pending_regen,omitempty"`

	// Publish records the outcome of pushing this article to an external CMS
	// (contentBridge). Nil until the first publish attempt.
	Publish *CGEPublishState `bson:"publish,omitempty"`

	// Target word count (calculated: avg competitor word count × 1.25)
	TargetWordCount int `bson:"target_word_count"`

	Status          int                `bson:"status"`
	ProcessMetadata CGEProcessMetadata `bson:"process_metadata"`
	ErrorData       []CGEErrorData     `bson:"error_data,omitempty"`

	CreatedAt time.Time `bson:"created_at,omitempty"`
	UpdatedAt time.Time `bson:"updated_at,omitempty"`
}

type CGESerpData struct {
	TopStructures          []CGEArticleStructure `bson:"top_structures,omitempty"`
	PAAQuestions           []string              `bson:"paa_questions,omitempty"`
	FeaturedSnippetPresent bool                  `bson:"featured_snippet_present"`
	FeaturedSnippetURL     string                `bson:"featured_snippet_url,omitempty"`
	AverageWordCount       int                   `bson:"average_word_count"`
}

type CGEArticleStructure struct {
	URL             string   `bson:"url"`
	H1              string   `bson:"h1,omitempty"`
	H2s             []string `bson:"h2s,omitempty"`
	H3s             []string `bson:"h3s,omitempty"`
	WordCount       int      `bson:"word_count,omitempty"`
	MetaDescription string   `bson:"meta_description,omitempty"`
}

type CGETopicResearch struct {
	RecentNews     *CGETopicInsight `bson:"recent_news,omitempty"`
	ExpertOpinion  *CGETopicInsight `bson:"expert_opinion,omitempty"`
	CommonMistakes *CGETopicInsight `bson:"common_mistakes,omitempty"`
}

type CGETopicInsight struct {
	Insight    string `bson:"insight"`
	Source     string `bson:"source"`
	SourceName string `bson:"source_name"`
}

type CGEYouTubeInsights struct {
	Insights []CGEYouTubeInsight `bson:"insights,omitempty" json:"youtube_insights"`
}

type CGEYouTubeInsight struct {
	Insight    string `bson:"insight"`
	VideoTitle string `bson:"video_title"`
	Channel    string `bson:"channel"`
}

type CGESerpGapAnalysis struct {
	CoveredByAll               []string `bson:"covered_by_all"`
	GapsIdentified             []string `bson:"gaps_identified"`
	FeaturedSnippetOpportunity string   `bson:"featured_snippet_opportunity"`
	DifferentiatingAngle       string   `bson:"differentiating_angle"`
}

type CGEOutline struct {
	RawJSON string `bson:"raw_json"`
	// URLSlug is the SEO slug picked out of the outline at generation time
	// (normalised: lowercase, hyphen-separated, stop words removed). It is the
	// default slug used for publishing until the user edits it.
	URLSlug string `bson:"url_slug,omitempty"`
}

// Slug returns the URL slug picked out of the outline. It prefers the
// structured URLSlug field (populated at generation time) and falls back to
// parsing "url_slug" out of RawJSON for outlines generated before the field
// existed. Returns "" when the outline is nil or carries no slug.
func (o *CGEOutline) Slug() string {
	if o == nil {
		return ""
	}
	if o.URLSlug != "" {
		return o.URLSlug
	}
	var parsed struct {
		URLSlug string `json:"url_slug"`
	}
	if err := json.Unmarshal([]byte(o.RawJSON), &parsed); err == nil {
		return parsed.URLSlug
	}
	return ""
}

type CGEImage struct {
	Position string `bson:"position"` // "thumbnail" or "mid-article"
	S3Key    string `bson:"s3_key"`
	Alt      string `bson:"alt"`
}

type CGESchemaMarkup struct {
	ArticleSchema json.RawMessage `bson:"article_schema" json:"article_schema"`
	FAQSchema     json.RawMessage `bson:"faq_schema" json:"faq_schema"`
}

type CGEMetaAssets struct {
	MetaTitle       string `bson:"meta_title" json:"meta_title"`
	MetaDescription string `bson:"meta_description" json:"meta_description"`
	SocialExcerpt   string `bson:"social_excerpt" json:"social_excerpt"`
}

// CGEEdited captures the user's draft edits made from the review screen.
// Each field overrides the pipeline-produced value of the same name; empty
// strings are treated as "cleared by the user" and still take precedence
// when the struct itself is non-nil.
type CGEEdited struct {
	ArticleContent  string `bson:"article_content,omitempty"`
	MetaTitle       string `bson:"meta_title,omitempty"`
	MetaDescription string `bson:"meta_description,omitempty"`
	URLSlug         string `bson:"url_slug,omitempty"`
}

// Publish status values stored on CGEPublishState.Status.
const (
	CGEPublishStatusPending   = "pending"
	CGEPublishStatusPublished = "published"
	CGEPublishStatusFailed    = "failed"
)

// CGEPublishState records the outcome of pushing this article to an external
// CMS. RemoteItemID is the idempotency key: when set, a re-publish updates the
// existing item instead of creating a duplicate.
type CGEPublishState struct {
	Status       string    `bson:"status"` // "pending" | "published" | "failed"
	Platform     string    `bson:"platform,omitempty"`
	RemoteItemID string    `bson:"remote_item_id,omitempty"`
	RemoteURL    string    `bson:"remote_url,omitempty"`
	PublishedAt  time.Time `bson:"published_at,omitempty"`
	LastError    string    `bson:"last_error,omitempty"`
	Attempts     int       `bson:"attempts,omitempty"`
	// SitePublished records whether the platform also ran a full site publish
	// (Framer: CMS items only go live after one).
	SitePublished bool `bson:"site_published,omitempty"`
	// Live records how the item was last pushed: true = live (draft:false +
	// site publish), false = draft. Drives the frontend "View live" vs
	// "Draft on Framer" affordance. See [[Publish Draft State]].
	Live bool `bson:"live,omitempty"`
	// NormalizedURL is the article's live URL canonicalized via
	// utils.NormalizeGSCPageURL — the join key GSC analytics ingest matches
	// page rows against to stamp article_id onto facts. Computed by
	// SetCGEPublishState (RemoteURL when present, else WebsiteURL + resolved
	// slug); legacy docs get it from cmd/normalizedurlmigrate.
	NormalizedURL string `bson:"normalized_url,omitempty"`
}

type CGEErrorData struct {
	Step    string `bson:"step"`
	Message string `bson:"message"`
}

type CGEProcessMetadata struct {
	ResearchStepStatus  CGEResearchStepStatus `bson:"research_step_status"`
	PostArticleStatus   CGEPostArticleStatus  `bson:"post_article_status"`
	UrlScrapeStatuses   []UrlScrapeStatus     `bson:"url_scrape_statuses,omitempty"`
	UrlScrapesRemaining int                   `bson:"url_scrapes_remaining"`

	// ImageGenPrompts records, keyed by position ("thumbnail" / "mid-article"),
	// the prompt sent to Haiku and the image prompt + alt text it returned.
	// Kept for observability/debugging of generated imagery.
	ImageGenPrompts map[string]CGEImageGenPrompt `bson:"image_gen_prompts,omitempty"`

	// Atomic dispatch flags for fan-in gates
	OutlineDispatched          bool `bson:"outline_dispatched"`
	ImageReplacementDispatched bool `bson:"image_replacement_dispatched"`
	FinalAssemblyDispatched    bool `bson:"final_assembly_dispatched"`
}

type CGEImageGenPrompt struct {
	Prompt      string `bson:"prompt"`
	ImagePrompt string `bson:"image_prompt"`
	AltText     string `bson:"alt_text"`
}

type CGEResearchStepStatus struct {
	SerpFetchDone         bool `bson:"serp_fetch_done"`
	UrlScrapeDone         bool `bson:"url_scrape_done"`
	SerpGapAnalysisDone   bool `bson:"serp_gap_analysis_done"`
	TavilyNewsDone        bool `bson:"tavily_news_done"`
	TavilyExpertDone      bool `bson:"tavily_expert_done"`
	TavilyMistakesDone    bool `bson:"tavily_mistakes_done"`
	YouTubeSearchDone     bool `bson:"youtube_search_done"`
	YouTubeTranscriptDone bool `bson:"youtube_transcript_done"`
	YouTubeSummaryDone    bool `bson:"youtube_summary_done"`
}

type CGEPostArticleStatus struct {
	// InternalLinkInsertionDone marks the internal-link-insertion step complete
	// (run or skipped). Gates re-runs on retry so links aren't double-inserted.
	InternalLinkInsertionDone bool `bson:"internal_link_insertion_done"`
	ImageGenThumbnailDone     bool `bson:"image_gen_thumbnail_done"`
	ImageGenMidArticleDone    bool `bson:"image_gen_mid_article_done"`
	ImageReplacementDone      bool `bson:"image_replacement_done"`
	SchemaGenerationDone      bool `bson:"schema_generation_done"`
	MetaAssetsGenerationDone  bool `bson:"meta_assets_generation_done"`
}

func (s *CGEPostArticleStatus) AllImageGenDone() bool {
	if s == nil {
		return false
	}
	return s.ImageGenThumbnailDone && s.ImageGenMidArticleDone
}

func (s *CGEPostArticleStatus) AllMetaDone() bool {
	if s == nil {
		return false
	}
	return s.SchemaGenerationDone && s.MetaAssetsGenerationDone
}

func (s *CGEResearchStepStatus) AllResearchDone() bool {
	if s == nil {
		return false
	}
	return s.SerpGapAnalysisDone &&
		s.TavilyNewsDone && s.TavilyExpertDone && s.TavilyMistakesDone &&
		s.YouTubeSummaryDone
}

type UrlScrapeStatus struct {
	URL    string `bson:"url"`
	Done   bool   `bson:"done"`
	Failed bool   `bson:"failed"`
}

type YouTubeVideoStatus struct {
	VideoID    string `bson:"video_id"`
	Title      string `bson:"title"`
	Channel    string `bson:"channel"`
	Transcript string `bson:"transcript,omitempty"`
	Done       bool   `bson:"done"`
	Failed     bool   `bson:"failed"`
	Skipped    bool   `bson:"skipped"`
}

// imageMarkdownRE matches a markdown image (`![alt](url)`). Used to exclude
// image tags from article word counts — they're embedded media, not prose.
var imageMarkdownRE = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

// articleImagePlaceholders are the template tokens the pipeline writes before
// image generation. Stripped from word counts so pre- and post-image-replacement
// counts stay comparable.
var articleImagePlaceholders = []string{"{{IMAGE_THUMBNAIL}}", "{{IMAGE_MID_ARTICLE}}"}

// CountArticleWords returns the whitespace-delimited word count of `content`
// with image markdown (`![alt](url)`) and image placeholders stripped first.
func CountArticleWords(content string) int {
	stripped := imageMarkdownRE.ReplaceAllString(content, "")
	for _, placeholder := range articleImagePlaceholders {
		stripped = strings.ReplaceAll(stripped, placeholder, "")
	}
	return len(strings.Fields(stripped))
}

func CreateWebEntityMasterContext(ctx context.Context, mc *WebEntityMasterContext) error {
	now := time.Now()
	mc.CreatedAt = now
	mc.UpdatedAt = now

	id, err := InsertOne(ctx, webEntityMasterContextCollection, mc)
	if err != nil {
		return err
	}
	mc.ID = id
	return nil
}

func GetWebEntityMasterContext(ctx context.Context, id string) (bool, *WebEntityMasterContext, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid masterContext ID: %w", err)
	}

	var mc WebEntityMasterContext
	isPresent, err := FindOne(ctx, webEntityMasterContextCollection, bson.M{"_id": objID}, &mc)
	if err != nil {
		return false, nil, fmt.Errorf("failed to find master context by ID: %w", err)
	}

	return isPresent, &mc, nil
}

// GetWebEntityMasterContextByScheduledArticleID returns the generated article
// document for a given calendar slot. Used by the article-by-schedule-id
// endpoint to surface CGE output to the review UI.
func GetWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *WebEntityMasterContext, error) {
	saOID, err := primitive.ObjectIDFromHex(scheduledArticleID)
	if err != nil {
		return false, nil, fmt.Errorf("invalid scheduled article ID: %w", err)
	}
	var mc WebEntityMasterContext
	isPresent, err := FindOne(ctx, webEntityMasterContextCollection, bson.M{
		"scheduled_article_id": saOID,
	}, &mc)
	if err != nil {
		return false, nil, fmt.Errorf("find master context by scheduled article id: %w", err)
	}
	return isPresent, &mc, nil
}

// DeleteWebEntityMasterContextByScheduledArticleID removes any generated master
// context tied to the given scheduled article. Articles that never finished
// generating have no master context, so a zero-match delete is treated as a
// no-op rather than an error.
func DeleteWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) error {
	saOID, err := primitive.ObjectIDFromHex(scheduledArticleID)
	if err != nil {
		return fmt.Errorf("invalid scheduled article ID: %w", err)
	}
	_, err = Collection(webEntityMasterContextCollection).DeleteMany(ctx, bson.M{
		"scheduled_article_id": saOID,
	})
	if err != nil {
		return fmt.Errorf("delete master context by scheduled article id: %w", err)
	}
	return nil
}

// FindMasterContextImageS3KeysByUserID projects images[].s3_key across every
// master context the user owns, for best-effort S3 cleanup in the admin
// deletion cascade. Empty keys are skipped.
func FindMasterContextImageS3KeysByUserID(ctx context.Context, userID primitive.ObjectID) ([]string, error) {
	cur, err := Collection(webEntityMasterContextCollection).Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetProjection(bson.M{"images.s3_key": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var rows []struct {
		Images []struct {
			S3Key string `bson:"s3_key"`
		} `bson:"images"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	var keys []string
	for _, r := range rows {
		for _, img := range r.Images {
			if img.S3Key != "" {
				keys = append(keys, img.S3Key)
			}
		}
	}
	return keys, nil
}

// DeleteWebEntityMasterContextsByUserID removes every master context the user
// owns. Admin deletion cascade only — first child deleted, after the S3 keys
// have been captured. Zero matches is a no-op so re-runs converge.
func DeleteWebEntityMasterContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(webEntityMasterContextCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// GetMasterContextsByScheduledArticleIDs batch-fetches master contexts for a
// set of scheduled-article IDs. Used by the calendar list endpoint to hydrate
// per-slot fields (word count, generated_at) without an N+1.
func GetMasterContextsByScheduledArticleIDs(ctx context.Context, scheduledArticleIDs []string) ([]WebEntityMasterContext, error) {
	if len(scheduledArticleIDs) == 0 {
		return nil, nil
	}
	oids := make([]primitive.ObjectID, 0, len(scheduledArticleIDs))
	for _, id := range scheduledArticleIDs {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("invalid scheduled article ID %q: %w", id, err)
		}
		oids = append(oids, oid)
	}
	cursor, err := Collection(webEntityMasterContextCollection).Find(ctx, bson.M{
		"scheduled_article_id": bson.M{"$in": oids},
	})
	if err != nil {
		return nil, fmt.Errorf("find master contexts by scheduled article ids: %w", err)
	}
	defer cursor.Close(ctx)

	var out []WebEntityMasterContext
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode master contexts by scheduled article ids: %w", err)
	}
	return out, nil
}

// GetWebEntityMasterContextByKeywordAndWEC finds an existing master context for a
// keyword + web entity context pair. Returns (false, nil, nil) when not found.
func GetWebEntityMasterContextByKeywordAndWEC(ctx context.Context, webEntityContextID string, keywordID primitive.ObjectID) (bool, *WebEntityMasterContext, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return false, nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	var mc WebEntityMasterContext
	isPresent, err := FindOne(ctx, webEntityMasterContextCollection, bson.M{
		"web_entity_context_id": wecOID,
		"keyword_id":            keywordID,
	}, &mc)
	if err != nil {
		return false, nil, fmt.Errorf("find master context by keyword and WEC: %w", err)
	}
	return isPresent, &mc, nil
}

// ResetCGEForRetry clears error data, sets status back to processing, and
// conditionally resets atomic dispatch flags only for steps whose output is
// still missing — so completed fan-in gates are not re-triggered.
func ResetCGEForRetry(ctx context.Context, id string, mc *WebEntityMasterContext) error {
	fields := bson.M{
		"error_data": bson.A{},
		"status":     CGEStatusProcessing,
	}

	if mc.Outline == nil {
		fields["process_metadata.outline_dispatched"] = false
	}
	if !mc.ProcessMetadata.PostArticleStatus.ImageReplacementDone {
		fields["process_metadata.image_replacement_dispatched"] = false
	}
	if !mc.ProcessMetadata.PostArticleStatus.AllMetaDone() {
		fields["process_metadata.final_assembly_dispatched"] = false
	}

	return setWEMCFields(ctx, id, fields)
}

// WEMCUpdateReq is the request struct for partial updates to WebEntityMasterContext.
type WEMCUpdateReq struct {
	SerpData            *CGESerpData
	TopicResearch       *CGETopicResearch
	YouTubeInsights     *CGEYouTubeInsights
	YouTubeVideos       []YouTubeVideoStatus
	SerpGapAnalysis     *CGESerpGapAnalysis
	Outline             *CGEOutline
	ArticleContent      *string
	FinalArticleContent *string
	WordCount           *int
	Images              []CGEImage
	SchemaMarkup        *CGESchemaMarkup
	MetaAssets          *CGEMetaAssets
	TargetWordCount     *int
	Status              *int
	ProcessMetadata     *CGEProcessMetadata
	Edited              *CGEEdited
}

func UpdateWebEntityMasterContext(ctx context.Context, id string, req WEMCUpdateReq) error {
	update := bson.M{}

	if req.SerpData != nil {
		update["serp_data"] = req.SerpData
	}
	if req.TopicResearch != nil {
		update["topic_research"] = req.TopicResearch
	}
	if req.YouTubeInsights != nil {
		update["youtube_insights"] = req.YouTubeInsights
	}
	if req.YouTubeVideos != nil {
		update["youtube_videos"] = req.YouTubeVideos
	}
	if req.SerpGapAnalysis != nil {
		update["serp_gap_analysis"] = req.SerpGapAnalysis
	}
	if req.Outline != nil {
		update["outline"] = req.Outline
	}
	if req.ArticleContent != nil {
		update["article_content"] = *req.ArticleContent
	}
	if req.FinalArticleContent != nil {
		update["final_article_content"] = *req.FinalArticleContent
	}
	if req.WordCount != nil {
		update["word_count"] = *req.WordCount
	}
	if req.Images != nil {
		update["images"] = req.Images
	}
	if req.SchemaMarkup != nil {
		update["schema_markup"] = req.SchemaMarkup
	}
	if req.MetaAssets != nil {
		update["meta_assets"] = req.MetaAssets
	}
	if req.Edited != nil {
		update["edited"] = req.Edited
	}
	if req.TargetWordCount != nil {
		update["target_word_count"] = *req.TargetWordCount
	}
	if req.Status != nil {
		update["status"] = *req.Status
	}
	if req.ProcessMetadata != nil {
		pm := req.ProcessMetadata
		s := &pm.ResearchStepStatus
		if s.SerpFetchDone {
			update["process_metadata.research_step_status.serp_fetch_done"] = true
		}
		if s.UrlScrapeDone {
			update["process_metadata.research_step_status.url_scrape_done"] = true
		}
		if s.SerpGapAnalysisDone {
			update["process_metadata.research_step_status.serp_gap_analysis_done"] = true
		}
		if s.TavilyNewsDone {
			update["process_metadata.research_step_status.tavily_news_done"] = true
		}
		if s.TavilyExpertDone {
			update["process_metadata.research_step_status.tavily_expert_done"] = true
		}
		if s.TavilyMistakesDone {
			update["process_metadata.research_step_status.tavily_mistakes_done"] = true
		}
		if s.YouTubeSearchDone {
			update["process_metadata.research_step_status.youtube_search_done"] = true
		}
		if s.YouTubeTranscriptDone {
			update["process_metadata.research_step_status.youtube_transcript_done"] = true
		}
		if s.YouTubeSummaryDone {
			update["process_metadata.research_step_status.youtube_summary_done"] = true
		}
		pa := &pm.PostArticleStatus
		if pa.InternalLinkInsertionDone {
			update["process_metadata.post_article_status.internal_link_insertion_done"] = true
		}
		if pa.ImageGenThumbnailDone {
			update["process_metadata.post_article_status.image_gen_thumbnail_done"] = true
		}
		if pa.ImageGenMidArticleDone {
			update["process_metadata.post_article_status.image_gen_mid_article_done"] = true
		}
		if pa.ImageReplacementDone {
			update["process_metadata.post_article_status.image_replacement_done"] = true
		}
		if pa.SchemaGenerationDone {
			update["process_metadata.post_article_status.schema_generation_done"] = true
		}
		if pa.MetaAssetsGenerationDone {
			update["process_metadata.post_article_status.meta_assets_generation_done"] = true
		}

		if pm.UrlScrapeStatuses != nil {
			update["process_metadata.url_scrape_statuses"] = pm.UrlScrapeStatuses
			update["process_metadata.url_scrapes_remaining"] = len(pm.UrlScrapeStatuses)
		}
	}

	if len(update) == 0 {
		return nil
	}

	return setWEMCFields(ctx, id, update)
}

func ClearCGEErrorData(ctx context.Context, id string) error {
	return setWEMCFields(ctx, id, bson.M{"error_data": bson.A{}})
}

// CGEPendingRegen — see WebEntityMasterContext.PendingRegen.
type CGEPendingRegen struct {
	Sections []CGEPendingSectionRegen        `bson:"sections,omitempty"`
	Images   map[string]CGEPendingImageRegen `bson:"images,omitempty"` // keyed by image position
}

// CGEPendingSectionRegen is one un-committed section rewrite. The content
// pair is what the client needs to re-apply the rewrite against the stored
// body (the old passage is re-located by content — positions are never
// persisted, the body may be edited underneath).
type CGEPendingSectionRegen struct {
	// ID is a unix-microsecond stamp assigned at regeneration time: unique per
	// article in practice, chronological when sorted, and small enough to ride
	// a JSON number losslessly.
	ID          int64  `bson:"id"`
	Label       string `bson:"label,omitempty"`
	OldMarkdown string `bson:"old_markdown"`
	NewMarkdown string `bson:"new_markdown"`
}

// CGEPendingImageRegen is one un-committed image regeneration: the versioned
// object waiting to be committed via save-draft's images[].newS3Key.
type CGEPendingImageRegen struct {
	S3Key string `bson:"s3_key"`
	Alt   string `bson:"alt,omitempty"`
}

// AppendPendingSectionRegen records an un-committed section rewrite.
func AppendPendingSectionRegen(ctx context.Context, id string, entry CGEPendingSectionRegen) error {
	return updateWEMCRaw(ctx, id, bson.M{
		"$push": bson.M{"pending_regen.sections": entry},
	})
}

// SetPendingImageRegen records (or replaces) the un-committed regeneration
// for one image position. A replaced entry's S3 object is deliberately left
// alone here — deleting it races a concurrent save that may have just
// committed it; repeated re-rolls orphan objects instead (same policy as
// abandoned user uploads).
func SetPendingImageRegen(ctx context.Context, id, position string, entry CGEPendingImageRegen) error {
	return setWEMCFields(ctx, id, bson.M{"pending_regen.images." + position: entry})
}

// RemovePendingSectionRegen discards one pending section rewrite (undo).
func RemovePendingSectionRegen(ctx context.Context, id string, sectionID int64) error {
	return updateWEMCRaw(ctx, id, bson.M{
		"$pull": bson.M{"pending_regen.sections": bson.M{"id": sectionID}},
	})
}

// RemovePendingImageRegen atomically discards the pending regeneration for
// one image position and returns the removed entry (nil when none existed) so
// the caller can garbage-collect its S3 object. The atomic remove-and-return
// is what makes that delete safe: after a save cleared the pending set, a
// late discard finds nothing and deletes nothing.
func RemovePendingImageRegen(ctx context.Context, id, position string) (*CGEPendingImageRegen, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid masterContext ID: %w", err)
	}
	field := "pending_regen.images." + position
	filter := bson.M{"_id": objID, field: bson.M{"$exists": true}}
	update := bson.M{
		"$unset": bson.M{field: ""},
		"$set":   bson.M{"updated_at": time.Now()},
	}
	var before WebEntityMasterContext
	err = Collection(webEntityMasterContextCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&before)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("remove pending image regen: %w", err)
	}
	if before.PendingRegen == nil {
		return nil, nil
	}
	entry, ok := before.PendingRegen.Images[position]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

// ClearPendingRegen drops the whole pending set — called when a draft save
// commits the regens (the body/images now carry them for real).
func ClearPendingRegen(ctx context.Context, id string) error {
	return updateWEMCRaw(ctx, id, bson.M{
		"$unset": bson.M{"pending_regen": ""},
	})
}

func AppendCGEErrorData(ctx context.Context, id string, errData CGEErrorData) error {
	return updateWEMCRaw(ctx, id, bson.M{
		"$push": bson.M{"error_data": errData},
		"$set": bson.M{
			"status": CGEStatusError,
		},
	})
}

func SetCGEStatusIfCurrent(ctx context.Context, id string, currentStatus, newStatus int) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}
	return UpdateOne(ctx, webEntityMasterContextCollection,
		bson.M{"_id": objID, "status": currentStatus},
		bson.M{"$set": bson.M{
			"status":     newStatus,
			"updated_at": time.Now(),
		}},
	)
}

// AtomicClaimOutlineDispatch attempts to atomically set outline_dispatched to true.
// Returns (true, nil) if this caller won the race.
func AtomicClaimOutlineDispatch(ctx context.Context, id string) (bool, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{"_id": objID, "process_metadata.outline_dispatched": false}
	update := bson.M{"$set": bson.M{
		"process_metadata.outline_dispatched": true,
		"updated_at":                          time.Now(),
	}}

	var result WebEntityMasterContext
	err = Collection(webEntityMasterContextCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&result)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, fmt.Errorf("atomic claim outline dispatch: %w", err)
	}

	return true, nil
}

// AtomicClaimImageReplacementDispatch attempts to atomically set image_replacement_dispatched to true.
// Returns (true, nil) if this caller won the race.
func AtomicClaimImageReplacementDispatch(ctx context.Context, id string) (bool, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{"_id": objID, "process_metadata.image_replacement_dispatched": false}
	update := bson.M{"$set": bson.M{
		"process_metadata.image_replacement_dispatched": true,
		"updated_at": time.Now(),
	}}

	var result WebEntityMasterContext
	err = Collection(webEntityMasterContextCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&result)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, fmt.Errorf("atomic claim image replacement dispatch: %w", err)
	}

	return true, nil
}

// AtomicClaimFinalAssemblyDispatch attempts to atomically set final_assembly_dispatched to true.
// Returns (true, nil) if this caller won the race.
func AtomicClaimFinalAssemblyDispatch(ctx context.Context, id string) (bool, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{"_id": objID, "process_metadata.final_assembly_dispatched": false}
	update := bson.M{"$set": bson.M{
		"process_metadata.final_assembly_dispatched": true,
		"updated_at": time.Now(),
	}}

	var result WebEntityMasterContext
	err = Collection(webEntityMasterContextCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&result)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, fmt.Errorf("atomic claim final assembly dispatch: %w", err)
	}

	return true, nil
}

// DecrementUrlScrapesRemaining atomically decrements url_scrapes_remaining by 1.
// Returns the remaining count after decrement. Only the caller that receives 0
// should proceed with finalization (fan-in gate).
func DecrementUrlScrapesRemaining(ctx context.Context, masterContextID string) (int, error) {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return -1, fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{"_id": objID, "process_metadata.url_scrapes_remaining": bson.M{"$gt": 0}}
	update := bson.M{
		"$inc": bson.M{"process_metadata.url_scrapes_remaining": -1},
		"$set": bson.M{"updated_at": time.Now()},
	}

	var result WebEntityMasterContext
	err = Collection(webEntityMasterContextCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&result)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return -1, fmt.Errorf("url scrapes remaining already 0 or document not found")
		}
		return -1, fmt.Errorf("decrement url scrapes remaining: %w", err)
	}

	return result.ProcessMetadata.UrlScrapesRemaining, nil
}

func SetCGEImageByPosition(ctx context.Context, masterContextID string, img CGEImage) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{
		"_id":             objID,
		"images.position": img.Position,
	}
	update := bson.M{
		"$set": bson.M{
			"images.$.s3_key": img.S3Key,
			"images.$.alt":    img.Alt,
			"updated_at":      time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

// SetCGEImageGenPrompt records the Haiku prompt and the image prompt + alt text
// it returned for a given position. Stored as a map keyed by position so the
// concurrent thumbnail/mid-article writes target distinct fields and don't
// collide.
func SetCGEImageGenPrompt(ctx context.Context, masterContextID, position string, p CGEImageGenPrompt) error {
	return setWEMCFields(ctx, masterContextID, bson.M{
		"process_metadata.image_gen_prompts." + position: p,
	})
}

func InitCGEImages(ctx context.Context, masterContextID string) error {
	images := []CGEImage{
		{Position: "thumbnail"},
		{Position: "mid-article"},
	}
	return setWEMCFields(ctx, masterContextID, bson.M{"images": images})
}

func MarkUrlScrapeDone(ctx context.Context, masterContextID string, url string) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{
		"_id": objID,
		"process_metadata.url_scrape_statuses.url": url,
	}
	update := bson.M{
		"$set": bson.M{
			"process_metadata.url_scrape_statuses.$.done": true,
			"updated_at": time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

func MarkUrlScrapeFailed(ctx context.Context, masterContextID string, url string) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{
		"_id": objID,
		"process_metadata.url_scrape_statuses.url": url,
	}
	update := bson.M{
		"$set": bson.M{
			"process_metadata.url_scrape_statuses.$.failed": true,
			"updated_at": time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

func MarkYouTubeTranscriptSkipped(ctx context.Context, masterContextID string, videoID string) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{
		"_id":                     objID,
		"youtube_videos.video_id": videoID,
	}
	update := bson.M{
		"$set": bson.M{
			"youtube_videos.$.skipped": true,
			"updated_at":               time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

// UpdateCGEArticleStructure performs a positional update on a single TopStructures
// entry matched by URL. Safe for concurrent calls targeting different URLs.
func UpdateCGEArticleStructure(ctx context.Context, masterContextID, url string, structure CGEArticleStructure) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}
	filter := bson.M{
		"_id":                          objID,
		"serp_data.top_structures.url": url,
	}
	update := bson.M{
		"$set": bson.M{
			"serp_data.top_structures.$.h1":               structure.H1,
			"serp_data.top_structures.$.h2s":              structure.H2s,
			"serp_data.top_structures.$.h3s":              structure.H3s,
			"serp_data.top_structures.$.word_count":       structure.WordCount,
			"serp_data.top_structures.$.meta_description": structure.MetaDescription,
			"updated_at": time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

func SetTopicResearchInsightAndMarkDone(ctx context.Context, masterContextID, variant string, insight *CGETopicInsight) error {
	var insightField, doneField string
	switch variant {
	case "news":
		insightField = "topic_research.recent_news"
		doneField = "process_metadata.research_step_status.tavily_news_done"
	case "expert":
		insightField = "topic_research.expert_opinion"
		doneField = "process_metadata.research_step_status.tavily_expert_done"
	case "mistakes":
		insightField = "topic_research.common_mistakes"
		doneField = "process_metadata.research_step_status.tavily_mistakes_done"
	default:
		return fmt.Errorf("unknown tavily variant: %s", variant)
	}
	return setWEMCFields(ctx, masterContextID, bson.M{
		insightField: insight,
		doneField:    true,
	})
}

func SaveYouTubeTranscriptAndMarkDone(ctx context.Context, masterContextID, videoID, transcript string) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}

	filter := bson.M{
		"_id":                     objID,
		"youtube_videos.video_id": videoID,
	}
	update := bson.M{
		"$set": bson.M{
			"youtube_videos.$.transcript": transcript,
			"youtube_videos.$.done":       true,
			"updated_at":                  time.Now(),
		},
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, filter, update)
}

// SetCGEPublishState writes/overwrites the whole publish block for a master
// context. Used by contentBridge to persist the result (pending/published/
// failed) of a publish attempt. Additionally stamps publish.normalized_url —
// the GSC analytics join key — since this write replaces the whole sub-doc.
func SetCGEPublishState(ctx context.Context, id string, state CGEPublishState) error {
	state.NormalizedURL = resolvePublishNormalizedURL(ctx, id, state)
	return setWEMCFields(ctx, id, bson.M{"publish": state})
}

// resolvePublishNormalizedURL computes the canonical article URL for a publish
// state: the platform-reported RemoteURL when it's a public page, else the
// master context's website URL + resolved slug (edited slug → outline slug).
// Both paths run through utils.NormalizeGSCPageURL so the stored value is
// symmetric with normalized GSC page rows. Best-effort: "" when nothing
// resolves — a publish write is never blocked by URL normalization.
func resolvePublishNormalizedURL(ctx context.Context, masterContextID string, state CGEPublishState) string {
	if remote := PublicRemoteURL(state.RemoteURL); remote != "" {
		return utils.NormalizeGSCPageURL(remote)
	}
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return ""
	}
	var mc WebEntityMasterContext
	found, err := FindOne(ctx, webEntityMasterContextCollection, bson.M{"_id": objID}, &mc)
	if err != nil || !found {
		return ""
	}
	slug := mc.EffectiveSlug()
	if mc.WebsiteURL == "" || slug == "" {
		return ""
	}
	return utils.NormalizeGSCPageURL(strings.TrimRight(mc.WebsiteURL, "/") + "/" + slug)
}

// PublicRemoteURL filters a platform-reported RemoteURL down to URLs that can
// actually appear in search results. Headless CMSes report their MANAGEMENT
// link — Payload's sidecar returns <base>/admin/collections/<coll>/<id>, the
// only URL the CMS knows — and stamping it broke the GSC article join
// (2026-08-26 finding: the one published article was keyed on /admin/…).
// Returns "" for admin-panel URLs so derivation falls through to the
// website-URL + slug path.
func PublicRemoteURL(remote string) string {
	if remote == "" {
		return ""
	}
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	first := strings.ToLower(strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 2)[0])
	if first == "admin" || first == "wp-admin" {
		return ""
	}
	return remote
}

// EffectiveSlug resolves the publish slug: the user's edited URL slug when
// set, else the slug picked out of the outline at generation time. An empty
// edited slug is not treated as "cleared" (a publish always needs a slug).
// The publish path (contentBridge) and the normalized-URL stamp both resolve
// through here so the precedence never drifts.
func (mc *WebEntityMasterContext) EffectiveSlug() string {
	if mc == nil {
		return ""
	}
	if mc.Edited != nil && mc.Edited.URLSlug != "" {
		return mc.Edited.URLSlug
	}
	return mc.Outline.Slug()
}

// FindPublishedNormalizedURLIndex maps publish.normalized_url →
// scheduled_article_id across the given WECs — the analytics ingest's
// article-stamping lookup, resolved ONCE per ingest run (NormalizeContext).
// Docs without a scheduled article are skipped; on duplicate URLs the last doc
// wins (a slot's master context is deleted and regenerated on retry, so
// transient duplicates are expected and harmless).
func FindPublishedNormalizedURLIndex(ctx context.Context, wecIDs []primitive.ObjectID) (map[string]primitive.ObjectID, error) {
	urlIndex := map[string]primitive.ObjectID{}
	if len(wecIDs) == 0 {
		return urlIndex, nil
	}
	cur, err := Collection(webEntityMasterContextCollection).Find(ctx,
		bson.M{
			"web_entity_context_id":  bson.M{"$in": wecIDs},
			"publish.normalized_url": bson.M{"$exists": true, "$gt": ""},
		},
		options.Find().SetProjection(bson.M{"publish.normalized_url": 1, "scheduled_article_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("find published normalized urls: %w", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ScheduledArticleID primitive.ObjectID `bson:"scheduled_article_id"`
		Publish            struct {
			NormalizedURL string `bson:"normalized_url"`
		} `bson:"publish"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode published normalized urls: %w", err)
	}
	for _, r := range rows {
		if r.ScheduledArticleID.IsZero() || r.Publish.NormalizedURL == "" {
			continue
		}
		urlIndex[r.Publish.NormalizedURL] = r.ScheduledArticleID
	}
	return urlIndex, nil
}

// FindPublishedSlugIndex maps published articles' effective slugs →
// scheduled_article_id across the given WECs — the GSC article-stamping
// FALLBACK join. Headless publishes can't know the public blog path prefix
// (the post lives at /blog/<slug> but only <slug> is ours), so ingest matches
// any page whose last path segment equals a published slug when the exact
// normalized-URL lookup misses. On duplicate slugs the last doc wins,
// mirroring the URL index.
func FindPublishedSlugIndex(ctx context.Context, wecIDs []primitive.ObjectID) (map[string]primitive.ObjectID, error) {
	slugIndex := map[string]primitive.ObjectID{}
	if len(wecIDs) == 0 {
		return slugIndex, nil
	}
	cur, err := Collection(webEntityMasterContextCollection).Find(ctx,
		bson.M{
			"web_entity_context_id": bson.M{"$in": wecIDs},
			"publish.status":        CGEPublishStatusPublished,
			"scheduled_article_id":  bson.M{"$exists": true},
		},
		options.Find().SetProjection(bson.M{"edited": 1, "outline": 1, "scheduled_article_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("find published slugs: %w", err)
	}
	defer cur.Close(ctx)
	var rows []WebEntityMasterContext
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode published slugs: %w", err)
	}
	for i := range rows {
		slug := strings.ToLower(strings.TrimSpace(rows[i].EffectiveSlug()))
		if slug == "" || rows[i].ScheduledArticleID.IsZero() {
			continue
		}
		slugIndex[slug] = rows[i].ScheduledArticleID
	}
	return slugIndex, nil
}

func setWEMCFields(ctx context.Context, masterContextID string, fields bson.M) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}
	fields["updated_at"] = time.Now()
	return UpdateOne(ctx, webEntityMasterContextCollection, bson.M{"_id": objID}, bson.M{"$set": fields})
}

func updateWEMCRaw(ctx context.Context, masterContextID string, update bson.M) error {
	objID, err := primitive.ObjectIDFromHex(masterContextID)
	if err != nil {
		return fmt.Errorf("invalid masterContext ID: %w", err)
	}
	if setFields, ok := update["$set"].(bson.M); ok {
		setFields["updated_at"] = time.Now()
	} else {
		update["$set"] = bson.M{"updated_at": time.Now()}
	}
	return UpdateOne(ctx, webEntityMasterContextCollection, bson.M{"_id": objID}, update)
}
