package dto

import (
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

// ArticleStatusError is the wire-format label surfaced when the linked
// WebEntityMasterContext is in CGEStatusError. The article doc itself stays
// at ScheduledArticleStatusGenerating — the "error" state is derived at
// read time so a successful retry naturally flips back to "generating"
// without touching the article doc. Mirrors the frontend's
// ARTICLE_STATUSES "error" label.
const ArticleStatusError = "error"

// ScheduledArticle is the calendar-facing view of a models.ScheduledArticle.
// It drops internal plumbing fields (user/context IDs, timestamps) the
// dashboard calendar doesn't need.
type ScheduledArticle struct {
	ID                     string `json:"id"`
	SequenceID             int    `json:"sequenceId"`
	WebEntityID            string `json:"webEntityId"`
	Keyword                string `json:"keyword"`
	Title                  string `json:"title,omitempty"`
	ArticleType            string `json:"articleType,omitempty"`
	Reasoning              string `json:"reasoning,omitempty"`
	AdditionalInstructions string `json:"additionalInstructions,omitempty"`
	// TitleSuggestions is the cached set of alternative titles (empty until the
	// dashboard "Regenerate suggestions" control has been run for this article).
	// Surfaced so the edit sidebar can reveal them without re-running the LLM.
	TitleSuggestions []string `json:"titleSuggestions,omitempty"`
	// Keyword-derived stats surfaced for the calendar / dashboard edit sidebar.
	// Hydrated by the service via a $in lookup against the keyword collection
	// and always populated from the keyword doc the article references —
	// independent of generation state. Zero values when the referenced keyword
	// is missing (defensive; should not happen in steady state).
	Volume     int     `json:"volume"`
	Difficulty int     `json:"difficulty"`
	CPC        float64 `json:"cpc"`
	// Funnel is the TOFU/MOFU/BOFU label as a string so the frontend can use
	// it directly with its existing funnel-badge mapping. Empty when the
	// referenced keyword has no funnel set yet.
	Funnel  string `json:"funnel,omitempty"`
	Intent  string `json:"intent,omitempty"`
	Cluster string `json:"cluster,omitempty"`
	// InternalLinkingEnabled is the per-article toggle for the internal-link
	// insertion generation step. Defaults to true for slots with no stored
	// value. Editable from the dashboard edit sidebar while still scheduled.
	InternalLinkingEnabled bool `json:"internalLinkingEnabled"`
	// PublishAsLive is the per-article live/draft override, exposed raw
	// (nullable): nil = inherit the WebEntity generic default. The frontend
	// resolves `article.publishAsLive ?? webEntity.publishAsLive ?? false` and
	// needs to see "unset" to track inheritance. See [[Publish Draft State]].
	PublishAsLive *bool `json:"publishAsLive,omitempty"`
	// ThumbnailStyle is the per-article thumbnail-style override, exposed raw
	// (nullable): absent = inherit the WebEntity default. The dashboard sidebar
	// hydrates its picker from this and resolves the "Workspace default
	// (currently X)" label against the WebEntity's resolved style.
	ThumbnailStyle *string   `json:"thumbnailStyle,omitempty"`
	ScheduleDate   time.Time `json:"scheduleDate"`
	// PublishAt is the same-day auto-publish target (edit-time + 1h), present
	// only for slots scheduled / rescheduled to today. Absent for future-dated
	// slots. Distinct from ScheduleDate (always 00:00 UTC of the calendar day).
	PublishAt *time.Time `json:"publishAt,omitempty"`
	// Status is the lifecycle label (scheduled / generating / readyForReview
	// / draft / published). Serialised as a string so the frontend can use it
	// directly with its status badge mapping.
	Status string `json:"status"`
	// WordCount is the master-context stored word count of the article body
	// (image markdown and image placeholders excluded). 0 until the pipeline
	// produces content.
	WordCount int `json:"wordCount"`
	// Publish is the content-bridge outcome for this slot. Absent until the
	// first publish attempt completes. Populated from the master context the
	// list path already batch-fetches to derive the "error" status, so it costs
	// no extra query. Lets the calendar / articles pollers settle publish state
	// (published / failed + remoteUrl / live / lastError) from the list payload.
	Publish *ArticlePublishState `json:"publish,omitempty"`
}

// FromDbModel populates the DTO from the persisted ScheduledArticle plus the
// keyword doc it references. The keyword arg is required to surface the
// keyword text and sequence id — both fields now live on the keyword
// collection rather than denormalised on the article.
func (a *ScheduledArticle) FromDbModel(m *models.ScheduledArticle, kw *models.Keyword) {
	a.ID = m.ID.Hex()
	a.WebEntityID = m.WebEntityID.Hex()
	a.Title = m.Title
	a.ArticleType = string(m.ArticleType)
	a.Reasoning = m.Reasoning
	a.AdditionalInstructions = m.AdditionalInstructions
	a.TitleSuggestions = m.TitleSuggestions
	// Unset (legacy / not-yet-backfilled) slots inherit the on-by-default rule.
	a.InternalLinkingEnabled = m.InternalLinkingEnabled == nil || *m.InternalLinkingEnabled
	// Exposed raw so the frontend can distinguish "inherit" (nil) from an
	// explicit override when resolving the effective live/draft state.
	a.PublishAsLive = m.PublishAsLive
	// Raw nullable — absent = inherit the WebEntity default.
	a.ThumbnailStyle = m.ThumbnailStyle
	a.ScheduleDate = m.ScheduleDate
	a.PublishAt = m.PublishAt
	a.Status = m.Status.String()
	if kw != nil {
		a.SequenceID = kw.SequenceID
		a.Keyword = kw.Keyword
		a.Volume = kw.Volume
		a.Difficulty = kw.KeywordDifficulty
		a.CPC = kw.CPC
		a.Funnel = string(kw.Funnel)
		a.Intent = kw.Intent
		a.Cluster = kw.Cluster
	}
}

// ScheduledArticlesResponse is the payload for the scheduled-articles endpoint.
// From is the inclusive start of the resolved window and To is its exclusive
// end — both echoed back so the frontend knows which range it actually got
// (relevant when from/to were omitted and the current week was used).
type ScheduledArticlesResponse struct {
	From     time.Time          `json:"from"`
	To       time.Time          `json:"to"`
	Articles []ScheduledArticle `json:"articles"`
}

// ScheduledArticleStatus is the compact polling view of a scheduled article —
// only the fields a live poller needs to settle a row: the lifecycle status,
// the (possibly just-generated) title, the word count, the schedule / publish
// timing, and the content-bridge publish outcome. Keyword-derived analytics
// (volume / difficulty / cpc / funnel / intent / cluster) are omitted because
// they are static per slot and never change while polling, so the poll path
// skips the keyword fetch entirely. This is a deliberately explicit contract:
// pollers merge exactly these fields into their fuller cached rows.
type ScheduledArticleStatus struct {
	ID           string               `json:"id"`
	Status       string               `json:"status"`
	Title        string               `json:"title,omitempty"`
	WordCount    int                  `json:"wordCount"`
	ScheduleDate time.Time            `json:"scheduleDate"`
	PublishAt    *time.Time           `json:"publishAt,omitempty"`
	Publish      *ArticlePublishState `json:"publish,omitempty"`
}

// FromDbModel populates the slim status view from the persisted article. No
// keyword doc is needed — the status view carries none of the keyword-derived
// fields. WordCount / Publish are filled by the service from the master context.
func (a *ScheduledArticleStatus) FromDbModel(m *models.ScheduledArticle) {
	a.ID = m.ID.Hex()
	a.Title = m.Title
	a.ScheduleDate = m.ScheduleDate
	a.PublishAt = m.PublishAt
	a.Status = m.Status.String()
}

// ScheduledArticlesStatusResponse is the compact list payload returned when the
// caller passes `view=status`. Mirrors ScheduledArticlesResponse's window echo
// but carries slim rows.
type ScheduledArticlesStatusResponse struct {
	From     time.Time                `json:"from"`
	To       time.Time                `json:"to"`
	Articles []ScheduledArticleStatus `json:"articles"`
}

type ArticleImage struct {
	Position string `json:"position"`
	S3Key    string `json:"s3Key,omitempty"`
	Alt      string `json:"alt,omitempty"`
}

type ArticleMeta struct {
	MetaTitle       string `json:"metaTitle,omitempty"`
	MetaDescription string `json:"metaDescription,omitempty"`
	SocialExcerpt   string `json:"socialExcerpt,omitempty"`
}

type ArticleSchema struct {
	ArticleSchema interface{} `json:"articleSchema,omitempty"`
	FAQSchema     interface{} `json:"faqSchema,omitempty"`
}

// ArticlePublishState is the wire view of the persisted CGEPublishState. The
// frontend polls it after firing POST /v1/content-bridge/publish to surface
// progress, the live URL, or the failure reason.
type ArticlePublishState struct {
	Status      string     `json:"status"` // "pending" | "published" | "failed"
	Platform    string     `json:"platform,omitempty"`
	RemoteURL   string     `json:"remoteUrl,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	Attempts    int        `json:"attempts,omitempty"`
	// Live reports how the item was last pushed: true = live, false = draft.
	// Gates the frontend "View live" vs "Draft on Framer" affordance.
	Live bool `json:"live"`
}

// FromDbModel maps the persisted publish block. Returns nil for a nil input
// so callers can assign the result unconditionally.
func PublishStateFromDbModel(m *models.CGEPublishState) *ArticlePublishState {
	if m == nil {
		return nil
	}
	out := &ArticlePublishState{
		Status:    m.Status,
		Platform:  m.Platform,
		RemoteURL: m.RemoteURL,
		LastError: m.LastError,
		Attempts:  m.Attempts,
		Live:      m.Live,
	}
	if !m.PublishedAt.IsZero() {
		t := m.PublishedAt
		out.PublishedAt = &t
	}
	return out
}

// ArticleDetailResponse is the payload for the article-by-schedule-id
// endpoint. Pairs calendar metadata with the generated content so the review
// UI can render everything from a single fetch. Returns the scheduled
// article even when generation has not produced content yet — `content` will
// simply be empty in that case.
type ArticleDetailResponse struct {
	ScheduledArticle ScheduledArticle `json:"scheduledArticle"`

	Funnel           string  `json:"funnel,omitempty"`
	Cluster          string  `json:"cluster,omitempty"`
	Intent           string  `json:"intent,omitempty"`
	OpportunityScore float64 `json:"opportunityScore"`
	Volume           int     `json:"volume"`
	Difficulty       int     `json:"difficulty"`
	CPC              float64 `json:"cpc"`

	Destination string `json:"destination,omitempty"`

	// URLSlug keeps its placeholder sentinel until the user edits a slug.
	// LiveURL / PublishedAt are filled from the persisted publish state once
	// the article has been published; sentinel strings before that.
	URLSlug     string `json:"urlSlug"`
	LiveURL     string `json:"liveUrl"`
	PublishedAt string `json:"publishedAt"`

	// Publish is the persisted outcome of the content-bridge publish flow.
	// Nil until the first publish attempt.
	Publish *ArticlePublishState `json:"publish,omitempty"`

	// WordCount is the master-context stored word count of the article body
	// (image markdown and image placeholders excluded). Recomputed whenever
	// the article body is written; 0 until the pipeline produces content.
	WordCount int `json:"wordCount"`

	// GeneratedAt is the master-context updated_at timestamp when content has
	// been produced; nil while the pipeline is still running.
	GeneratedAt *time.Time `json:"generatedAt,omitempty"`

	// SchemaGenerated mirrors the frontend's `schema.generated` flag — true
	// iff both article and FAQ schema blocks are present.
	SchemaGenerated bool `json:"schemaGenerated"`

	Content string         `json:"content,omitempty"`
	Meta    *ArticleMeta   `json:"meta,omitempty"`
	Schema  *ArticleSchema `json:"schema,omitempty"`
	Images  []ArticleImage `json:"images,omitempty"`

	// PendingRegens carries un-committed regenerations awaiting the user's
	// save-or-undo decision — the client restores them into the editor on
	// load. Nil when none are pending.
	PendingRegens *ArticlePendingRegens `json:"pendingRegens,omitempty"`
}

// ArticlePendingRegens mirrors models.CGEPendingRegen for the wire.
type ArticlePendingRegens struct {
	Sections []ArticlePendingSectionRegen        `json:"sections,omitempty"`
	Images   map[string]ArticlePendingImageRegen `json:"images,omitempty"`
}

// ArticlePendingSectionRegen is one un-committed section rewrite. The client
// re-locates OldMarkdown by content in the loaded body and splices
// NewMarkdown on top; ID keys the discard call.
type ArticlePendingSectionRegen struct {
	ID          int64  `json:"id"`
	Label       string `json:"label,omitempty"`
	OldMarkdown string `json:"oldMarkdown"`
	NewMarkdown string `json:"newMarkdown"`
}

// ArticlePendingImageRegen is one un-committed image regeneration: URL for
// the editor preview, NewS3Key to echo back on save-draft to commit it.
type ArticlePendingImageRegen struct {
	NewS3Key string `json:"newS3Key"`
	URL      string `json:"url"`
	Alt      string `json:"alt,omitempty"`
}
