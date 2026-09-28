package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	idto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/dto"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Sentinel returned for fields the publishing flow hasn't shipped yet
// (url slug, live url, published-at). Replaced once persistence lands.
const placeholderField = "xxxxxxxxxxxxxxxxxxx"

// userUploadedPrefix is the S3 key namespace for images the user uploaded to
// replace a generated one. Kept distinct from the pipeline's `generated_images/`
// folder so user content is easy to identify and lifecycle separately.
const userUploadedPrefix = "user-uploaded/"

// imageContentTypeExt maps the content types we accept for a replacement image
// to the file extension used in the S3 key. Doubles as the allow-list — a
// content type absent from this map is rejected.
var imageContentTypeExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// validImagePositions is the set of image slots a generated article carries.
var validImagePositions = map[string]struct{}{
	"thumbnail":   {},
	"mid-article": {},
}

var _ scheduledArticleService.ScheduledArticleService = (*svc)(nil)

type svc struct {
	store    store.Store
	s3       interfaces.S3
	s3Bucket string
}

func NewService(s store.Store, s3 interfaces.S3, s3Bucket string) scheduledArticleService.ScheduledArticleService {
	return &svc{store: s, s3: s3, s3Bucket: s3Bucket}
}

func (s *svc) GetScheduledArticles(ctx context.Context, userId, webEntityId string, from, to *time.Time) (*dto.ScheduledArticlesResponse, error) {
	articles, start, endExclusive, err := s.loadArticlesInWindow(ctx, userId, webEntityId, from, to)
	if err != nil {
		return nil, err
	}

	keywordByID, err := s.hydrateKeywords(ctx, articles)
	if err != nil {
		return nil, err
	}

	mcByScheduledID, err := s.hydrateMasterContexts(ctx, articles)
	if err != nil {
		return nil, err
	}

	out := make([]dto.ScheduledArticle, len(articles))
	for i, a := range articles {
		kw := keywordByID[a.KeywordID]
		out[i].FromDbModel(a, kw)
		if mc, ok := mcByScheduledID[a.ID.Hex()]; ok {
			out[i].WordCount = mc.WordCount
			// CGE error surfaces as a wire-only "error" status so the UI can
			// offer a retry without persisting a parallel error state on the
			// article doc. Retry flips the master context back to processing,
			// after which this branch stops firing.
			if mc.Status == models.CGEStatusError {
				out[i].Status = dto.ArticleStatusError
			}
			// Publish outcome from the same batch-fetched master context — no
			// extra query. Nil until the first publish attempt completes.
			out[i].Publish = dto.PublishStateFromDbModel(mc.Publish)
		}
	}

	return &dto.ScheduledArticlesResponse{
		From:     start,
		To:       endExclusive,
		Articles: out,
	}, nil
}

// GetScheduledArticleStatuses is the compact counterpart of GetScheduledArticles
// used by the frontend polling path (`view=status`). It deliberately skips the
// keyword hydration entirely — the keyword-derived stats are static per slot and
// pollers never need them — and returns only the fields that can change while a
// row settles (status, title, word count, publish timing, publish outcome).
func (s *svc) GetScheduledArticleStatuses(ctx context.Context, userId, webEntityId string, from, to *time.Time) (*dto.ScheduledArticlesStatusResponse, error) {
	articles, start, endExclusive, err := s.loadArticlesInWindow(ctx, userId, webEntityId, from, to)
	if err != nil {
		return nil, err
	}

	// No hydrateKeywords: the slim view carries no keyword-derived fields.
	mcByScheduledID, err := s.hydrateMasterContexts(ctx, articles)
	if err != nil {
		return nil, err
	}

	out := make([]dto.ScheduledArticleStatus, len(articles))
	for i, a := range articles {
		out[i].FromDbModel(a)
		if mc, ok := mcByScheduledID[a.ID.Hex()]; ok {
			out[i].WordCount = mc.WordCount
			if mc.Status == models.CGEStatusError {
				out[i].Status = dto.ArticleStatusError
			}
			out[i].Publish = dto.PublishStateFromDbModel(mc.Publish)
		}
	}

	return &dto.ScheduledArticlesStatusResponse{
		From:     start,
		To:       endExclusive,
		Articles: out,
	}, nil
}

// loadArticlesInWindow verifies ownership of the web entity and returns the
// user's scheduled articles for the resolved [start, end) window. Shared by the
// full and compact (view=status) list paths so ownership + windowing stay
// identical between them.
func (s *svc) loadArticlesInWindow(ctx context.Context, userId, webEntityId string, from, to *time.Time) ([]*models.ScheduledArticle, time.Time, time.Time, error) {
	exists, entity, err := s.store.GetWebEntityByID(ctx, webEntityId)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("failed to look up web entity: %w", err)
	}
	if !exists || entity.UserID.Hex() != userId {
		return nil, time.Time{}, time.Time{}, scheduledArticleService.ErrWebEntityNotFound
	}

	start, endExclusive := resolveWindow(from, to)

	articles, err := s.store.GetScheduledArticlesByWebEntityInRange(ctx, userId, webEntityId, start, endExclusive)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("failed to look up scheduled articles: %w", err)
	}
	return articles, start, endExclusive, nil
}

// hydrateMasterContexts batches a $in fetch over master contexts for the given
// scheduled articles. The list-articles endpoint needs both the word count
// and the pipeline status (to derive the wire-only "error" label), so this
// returns the whole doc rather than a single field projection.
// Slots with no generated content yet are absent from the map.
func (s *svc) hydrateMasterContexts(ctx context.Context, articles []*models.ScheduledArticle) (map[string]*models.WebEntityMasterContext, error) {
	if len(articles) == 0 {
		return nil, nil
	}
	ids := make([]string, len(articles))
	for i, a := range articles {
		ids[i] = a.ID.Hex()
	}
	mcs, err := s.store.GetMasterContextsByScheduledArticleIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate master contexts: %w", err)
	}
	out := make(map[string]*models.WebEntityMasterContext, len(mcs))
	for i := range mcs {
		out[mcs[i].ScheduledArticleID.Hex()] = &mcs[i]
	}
	return out, nil
}

// hydrateKeywords batches a $in fetch for every keyword referenced by the
// given articles. Returns a lookup map keyed by keyword ObjectID.
func (s *svc) hydrateKeywords(ctx context.Context, articles []*models.ScheduledArticle) (map[primitive.ObjectID]*models.Keyword, error) {
	if len(articles) == 0 {
		return nil, nil
	}
	ids := make([]primitive.ObjectID, 0, len(articles))
	seen := make(map[primitive.ObjectID]struct{}, len(articles))
	for _, a := range articles {
		if _, ok := seen[a.KeywordID]; ok {
			continue
		}
		seen[a.KeywordID] = struct{}{}
		ids = append(ids, a.KeywordID)
	}
	keywords, err := s.store.GetKeywordsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate keywords: %w", err)
	}
	out := make(map[primitive.ObjectID]*models.Keyword, len(keywords))
	for i := range keywords {
		out[keywords[i].ID] = &keywords[i]
	}
	return out, nil
}

// resolveWindow turns the optional from/to dates into a half-open [start, end)
// instant range. from/to arrive as midnight-UTC dates; end is pushed to the
// start of the day after `to` so the `to` day is included. When either bound
// is missing, the current Monday–Sunday week (UTC) is used.
func resolveWindow(from, to *time.Time) (time.Time, time.Time) {
	if from == nil || to == nil {
		return currentWeekRange(time.Now())
	}
	start := from.UTC()
	endExclusive := to.UTC().AddDate(0, 0, 1)
	return start, endExclusive
}

func (s *svc) ResolveOrchestrateContext(ctx context.Context, userId, scheduledArticleID string) (*scheduledArticleService.OrchestrateContext, error) {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		// Collapse missing + unauthorised into the same error so callers
		// can't infer the existence of another user's slot.
		return nil, scheduledArticleService.ErrScheduledArticleNotFound
	}
	return &scheduledArticleService.OrchestrateContext{
		WebEntityContextID:     sa.WebEntityContextID.Hex(),
		KeywordID:              sa.KeywordID.Hex(),
		ArticleType:            string(sa.ArticleType),
		Title:                  sa.Title,
		AdditionalInstructions: sa.AdditionalInstructions,
		InternalLinkingEnabled: sa.InternalLinkingEnabled,
		ThumbnailStyle:         sa.ThumbnailStyle,
		Status:                 sa.Status,
	}, nil
}

func (s *svc) UpdateFromDashboard(ctx context.Context, userId, scheduledArticleID string, payload scheduledArticleService.DashboardEditPayload) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return scheduledArticleService.ErrScheduledArticleNotFound
	}

	// Status-level gating: only generating and published reject edits.
	// Past and future slots share the same editability — the calendar date
	// does not lock the row.
	switch sa.Status {
	case models.ScheduledArticleStatusGenerating, models.ScheduledArticleStatusPublished:
		return scheduledArticleService.ErrScheduledArticleNotEditable
	case models.ScheduledArticleStatusScheduling:
		// Enrichment still in flight — metadata isn't settled yet.
		return scheduledArticleService.ErrScheduledArticleNotEditable
	}

	todayStart := startOfDayUTC(time.Now())
	allowTitle, allowType, allowInstructions, allowDate := allowedFieldsForStatus(sa.Status)

	if payload.Title != nil && !allowTitle {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}
	if payload.ArticleType != nil && !allowType {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}
	if payload.AdditionalInstructions != nil && !allowInstructions {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}
	// Internal-linking toggle is a pre-generation brief setting — editable on the
	// same terms as additional instructions.
	if payload.InternalLinkingEnabled != nil && !allowInstructions {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}
	// Thumbnail style is a generation-time input — same gate as the brief fields;
	// a post-generation change would have no effect in v1.
	if payload.ThumbnailStyle != nil && !allowInstructions {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}
	if payload.ScheduleDate != nil && !allowDate {
		return scheduledArticleService.ErrFieldNotEditableForStatus
	}

	update := models.ScheduledArticleUpdateReq{}

	if payload.Title != nil {
		t := strings.TrimSpace(*payload.Title)
		if t == "" {
			// Title can't be cleared — it's the calendar-list label.
			return scheduledArticleService.ErrFieldNotEditableForStatus
		}
		update.Title = &t
	}
	if payload.ArticleType != nil {
		parsed, ok := models.ParseArticleType(*payload.ArticleType)
		if !ok {
			return scheduledArticleService.ErrInvalidArticleType
		}
		update.ArticleType = &parsed
		// Suggestions are title-format-specific; a type change invalidates the
		// cached set so the next "regenerate suggestions" produces titles for
		// the new format.
		cleared := []string{}
		update.TitleSuggestions = &cleared
		// Persist the reasoning regenerated for the new type alongside it. Only
		// honoured here (with a type change) since reasoning explains the type
		// choice. Empty/absent reasoning leaves the stored value untouched.
		if payload.Reasoning != nil {
			if r := strings.TrimSpace(*payload.Reasoning); r != "" {
				update.Reasoning = &r
			}
		}
	}
	if payload.AdditionalInstructions != nil {
		// Empty string is a legitimate clear here.
		instr := *payload.AdditionalInstructions
		update.AdditionalInstructions = &instr
	}
	if payload.InternalLinkingEnabled != nil {
		update.InternalLinkingEnabled = payload.InternalLinkingEnabled
	}
	if payload.ThumbnailStyle != nil {
		// Empty string is a legitimate reset-to-inherit; a non-empty value must
		// be a known catalog style. The store maps ""→$unset, else $set.
		style := *payload.ThumbnailStyle
		if style != "" && !prompts.IsValidThumbnailStyle(style) {
			return scheduledArticleService.ErrInvalidThumbnailStyle
		}
		update.ThumbnailStyle = &style
	}
	if payload.ScheduleDate != nil {
		newDate := startOfDayUTC(*payload.ScheduleDate)
		// Past, today, and future are all valid targets for unpublished rows.
		update.ScheduleDate = &newDate
		// Same-day reschedule: persist an auto-publish target an hour out so the
		// deferred publish runner has a concrete instant. Past/future dates leave
		// it nil (the runner picks the time for future; past needs no stamp).
		if newDate.Equal(todayStart) {
			publishAt := time.Now().Add(time.Hour)
			update.PublishAt = &publishAt
		}
	}

	// Nothing to write — treat as a successful no-op rather than a 4xx.
	if update.Title == nil && update.ArticleType == nil && update.AdditionalInstructions == nil &&
		update.ScheduleDate == nil && update.InternalLinkingEnabled == nil && update.ThumbnailStyle == nil {
		return nil
	}

	if err := s.store.UpdateScheduledArticle(ctx, scheduledArticleID, update); err != nil {
		return fmt.Errorf("update scheduled article from dashboard: %w", err)
	}
	return nil
}

// DeleteScheduledArticle removes a calendar slot and any generated master
// context tied to it, after verifying ownership. Generating articles are
// rejected so a delete can't race the running content pipeline.
func (s *svc) DeleteScheduledArticle(ctx context.Context, userId, scheduledArticleID string) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return scheduledArticleService.ErrScheduledArticleNotFound
	}
	if sa.Status == models.ScheduledArticleStatusGenerating {
		return scheduledArticleService.ErrScheduledArticleNotEditable
	}

	// Drop the generated content first so a failure here doesn't leave the slot
	// gone but its master context orphaned. The slot delete is the source of
	// truth for the calendar, so it goes last.
	if err := s.store.DeleteMasterContextByScheduledArticleID(ctx, scheduledArticleID); err != nil {
		return fmt.Errorf("delete master context: %w", err)
	}
	if err := s.store.DeleteScheduledArticle(ctx, scheduledArticleID); err != nil {
		return fmt.Errorf("delete scheduled article: %w", err)
	}
	return nil
}

// SetPublishState persists the per-article live/draft override after verifying
// ownership. Editable in every state except mid-generation — including
// published (Q9: the control drives Republish). This is the reason it uses a
// focused write path rather than UpdateFromDashboard, which rejects published.
func (s *svc) SetPublishState(ctx context.Context, userId, scheduledArticleID string, live bool) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return scheduledArticleService.ErrScheduledArticleNotFound
	}
	if sa.Status == models.ScheduledArticleStatusGenerating {
		return scheduledArticleService.ErrScheduledArticleNotEditable
	}
	return s.store.SetScheduledArticlePublishAsLive(ctx, scheduledArticleID, live)
}

// allowedFieldsForStatus returns the per-field edit permissions for the given
// status. Past and future slots share the same rules — only status gates edits.
func allowedFieldsForStatus(status models.ScheduledArticleStatus) (title, articleType, instructions, scheduleDate bool) {
	switch status {
	case models.ScheduledArticleStatusScheduled:
		return true, true, true, true
	case models.ScheduledArticleStatusDraft, models.ScheduledArticleStatusReadyForReview:
		return true, false, false, true
	default:
		return false, false, false, false
	}
}

func startOfDayUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *svc) GetArticleByScheduleID(ctx context.Context, userId, scheduledArticleID string) (*dto.ArticleDetailResponse, error) {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok {
		return nil, scheduledArticleService.ErrScheduledArticleNotFound
	}
	if sa.UserID.Hex() != userId {
		// Don't leak existence of other users' slots — return the same
		// not-found error rather than a forbidden.
		return nil, scheduledArticleService.ErrScheduledArticleNotFound
	}

	_, kw, err := s.store.GetKeyword(ctx, sa.KeywordID.Hex())
	if err != nil {
		return nil, fmt.Errorf("get keyword: %w", err)
	}

	resp := &dto.ArticleDetailResponse{
		URLSlug:     placeholderField,
		LiveURL:     placeholderField,
		PublishedAt: placeholderField,
	}
	resp.ScheduledArticle.FromDbModel(sa, kw)
	if kw != nil {
		resp.Funnel = string(kw.Funnel)
		resp.Cluster = kw.Cluster
		resp.Intent = kw.Intent
		resp.OpportunityScore = kw.OpportunityScore
		resp.Volume = kw.Volume
		resp.Difficulty = kw.KeywordDifficulty
		resp.CPC = kw.CPC
	}

	if _, entity, weErr := s.store.GetWebEntityByID(ctx, sa.WebEntityID.Hex()); weErr == nil && entity != nil && entity.Publishing != nil {
		resp.Destination = entity.Publishing.Platform
	}

	hasContent, mc, err := s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get generated article: %w", err)
	}
	if !hasContent {
		return resp, nil
	}

	// Surface CGE errors as the wire-only "error" status so the detail view
	// can show the retry affordance. See dto.ArticleStatusError.
	if mc.Status == models.CGEStatusError {
		resp.ScheduledArticle.Status = dto.ArticleStatusError
	}

	// Publish state (content bridge outcome): the frontend polls this after
	// firing a publish. LiveURL/PublishedAt swap their placeholder sentinels
	// for real values once published.
	resp.Publish = dto.PublishStateFromDbModel(mc.Publish)
	if mc.Publish != nil && mc.Publish.Status == models.CGEPublishStatusPublished {
		if mc.Publish.RemoteURL != "" {
			resp.LiveURL = mc.Publish.RemoteURL
		}
		if !mc.Publish.PublishedAt.IsZero() {
			resp.PublishedAt = mc.Publish.PublishedAt.Format(time.RFC3339)
		}
	}

	// `ArticleContent` is the post-image-replacement body — title and thumbnail
	// stripped, mid-article image inline. (`FinalArticleContent` exists on the
	// model but is currently never written by the pipeline.) The title and
	// thumbnail are sent as their own fields — the resolved title below and the
	// thumbnail-position entry in Images — and the review UI composes them back
	// onto the body for display. Keeping `Content` title/thumbnail-free is what
	// lets the review UI's "copy markdown" yield the body alone.
	//
	// User edits override the pipeline output: when the master context has
	// an `edited` block, OR the scheduled article is in `draft` status, we
	// return the edited values regardless of whether the underlying field
	// has been set. This is the source of truth contract — once the user
	// has saved a draft, what they typed wins.
	preferEdited := mc.Edited != nil || sa.Status == models.ScheduledArticleStatusDraft
	resp.Content = pickString(preferEdited, edited(mc).ArticleContent, fallbackContent(mc))
	// Resolve the displayed title once: the stored article title, falling back to
	// the pipeline's proposed title. Surfaced on the ScheduledArticle DTO so the
	// review UI has a single title source for both the header and the composed H1.
	if resp.ScheduledArticle.Title == "" {
		resp.ScheduledArticle.Title = mc.ProposedTitle
	}
	resp.WordCount = mc.WordCount
	resp.ScheduledArticle.WordCount = mc.WordCount
	generatedAt := mc.UpdatedAt
	resp.GeneratedAt = &generatedAt
	metaTitle, metaDesc, social := resolveMeta(preferEdited, mc)
	if metaTitle != "" || metaDesc != "" || social != "" {
		resp.Meta = &dto.ArticleMeta{
			MetaTitle:       metaTitle,
			MetaDescription: metaDesc,
			SocialExcerpt:   social,
		}
	}
	if preferEdited && edited(mc).URLSlug != "" {
		resp.URLSlug = edited(mc).URLSlug
	} else if outlineSlug := mc.Outline.Slug(); outlineSlug != "" {
		// Default to the slug picked out of the outline so the review UI shows
		// the pipeline slug until the user overrides it.
		resp.URLSlug = outlineSlug
	}
	if mc.SchemaMarkup != nil {
		article := rawJSONToAny(mc.SchemaMarkup.ArticleSchema)
		faq := rawJSONToAny(mc.SchemaMarkup.FAQSchema)
		resp.Schema = &dto.ArticleSchema{
			ArticleSchema: article,
			FAQSchema:     faq,
		}
		resp.SchemaGenerated = article != nil && faq != nil
	}
	if len(mc.Images) > 0 {
		resp.Images = make([]dto.ArticleImage, len(mc.Images))
		for i, img := range mc.Images {
			// Surface the full URL so the review UI can render the image
			// directly without re-implementing the bucket/region template.
			var url string
			if img.S3Key != "" {
				url = s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, img.S3Key)
			}
			resp.Images[i] = dto.ArticleImage{
				Position: img.Position,
				S3Key:    url,
				Alt:      img.Alt,
			}
		}
	}

	// Un-committed regenerations, restored into the editor on load so a regen
	// survives reloads and devices until the user saves or discards it.
	if pr := mc.PendingRegen; pr != nil && (len(pr.Sections) > 0 || len(pr.Images) > 0) {
		out := &dto.ArticlePendingRegens{}
		for _, sec := range pr.Sections {
			out.Sections = append(out.Sections, dto.ArticlePendingSectionRegen{
				ID:          sec.ID,
				Label:       sec.Label,
				OldMarkdown: sec.OldMarkdown,
				NewMarkdown: sec.NewMarkdown,
			})
		}
		if len(pr.Images) > 0 {
			out.Images = make(map[string]dto.ArticlePendingImageRegen, len(pr.Images))
			for position, img := range pr.Images {
				out.Images[position] = dto.ArticlePendingImageRegen{
					NewS3Key: img.S3Key,
					URL:      s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, img.S3Key),
					Alt:      img.Alt,
				}
			}
		}
		resp.PendingRegens = out
	}

	return resp, nil
}

// edited returns mc.Edited or an empty value — keeps call sites free of nil
// checks when reading individual fields.
func edited(mc *models.WebEntityMasterContext) models.CGEEdited {
	if mc.Edited == nil {
		return models.CGEEdited{}
	}
	return *mc.Edited
}

// fallbackContent picks the pipeline-produced content (FinalArticleContent
// would-be takes precedence but is currently never written, so this is
// effectively ArticleContent).
func fallbackContent(mc *models.WebEntityMasterContext) string {
	if mc.FinalArticleContent != "" {
		return mc.FinalArticleContent
	}
	return mc.ArticleContent
}

// pickString returns the user-edited value when `useEdited` is true, falling
// back to the pipeline value otherwise. Empty user edits are still respected
// when useEdited is true — the user explicitly cleared the field.
func pickString(useEdited bool, edited, pipeline string) string {
	if useEdited {
		return edited
	}
	return pipeline
}

// resolveMeta returns the meta-asset triple the API should expose, honouring
// the edited override when applicable. SocialExcerpt is never user-editable
// on the review screen, so it always comes from the pipeline.
func resolveMeta(useEdited bool, mc *models.WebEntityMasterContext) (string, string, string) {
	var pipeTitle, pipeDesc, pipeSocial string
	if mc.MetaAssets != nil {
		pipeTitle = mc.MetaAssets.MetaTitle
		pipeDesc = mc.MetaAssets.MetaDescription
		pipeSocial = mc.MetaAssets.SocialExcerpt
	}
	if useEdited {
		e := edited(mc)
		return e.MetaTitle, e.MetaDescription, pipeSocial
	}
	return pipeTitle, pipeDesc, pipeSocial
}

// CreateImageUploadURL issues a presigned PUT for a replacement image and the
// public URL it will be served at. The key lives under the user-uploaded
// namespace and is bound to the user + master context + position so SaveDraft
// can later validate that a returned key really came from here.
func (s *svc) CreateImageUploadURL(ctx context.Context, userId, scheduledArticleID, position, contentType string) (*scheduledArticleService.ImageUploadTarget, error) {
	ext, ok := imageContentTypeExt[strings.ToLower(strings.TrimSpace(contentType))]
	if !ok {
		return nil, scheduledArticleService.ErrInvalidImageContentType
	}
	if _, ok := validImagePositions[position]; !ok {
		return nil, scheduledArticleService.ErrInvalidImagePosition
	}

	found, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled article: %w", err)
	}
	if !found || sa.UserID.Hex() != userId {
		return nil, scheduledArticleService.ErrScheduledArticleNotFound
	}

	hasContent, mc, err := s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get master context: %w", err)
	}
	if !hasContent {
		return nil, scheduledArticleService.ErrArticleNotGenerated
	}

	// Unique key per upload (unix-nano suffix) so a re-upload of the same
	// position never collides with the object still referenced by the article
	// until SaveDraft swaps the pointer and deletes the old one.
	key := fmt.Sprintf("%s-%d.%s", userUploadedImageKeyPrefix(userId, mc.ID.Hex(), position), time.Now().UnixNano(), ext)

	presigned, err := s.s3.GenerateSinglepartUploadPresignedURLs(ctx, s.s3Bucket, []idto.PresignedSinglepartPutRequest{
		{Key: key, ContentType: contentType},
	})
	if err != nil {
		return nil, fmt.Errorf("presign image upload: %w", err)
	}
	if len(presigned) != 1 {
		return nil, fmt.Errorf("presign image upload: expected 1 url, got %d", len(presigned))
	}

	return &scheduledArticleService.ImageUploadTarget{
		Key:       presigned[0].Key,
		UploadURL: presigned[0].URL,
		Headers:   presigned[0].Headers,
		PublicURL: s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, key),
	}, nil
}

// CreateInlineImageUploadURL issues a presigned PUT for an image the user
// inserts inline into the article body via the editor toolbar, plus the public
// URL it will be served at. Unlike CreateImageUploadURL it is not tied to a
// structured image slot — the public URL is embedded directly into the saved
// article content — so no position is required and nothing is echoed back on
// SaveDraft. The key still lives under the user-uploaded namespace so uploads
// share the same lifecycle handling.
func (s *svc) CreateInlineImageUploadURL(ctx context.Context, userId, scheduledArticleID, contentType string) (*scheduledArticleService.ImageUploadTarget, error) {
	ext, ok := imageContentTypeExt[strings.ToLower(strings.TrimSpace(contentType))]
	if !ok {
		return nil, scheduledArticleService.ErrInvalidImageContentType
	}

	found, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled article: %w", err)
	}
	if !found || sa.UserID.Hex() != userId {
		return nil, scheduledArticleService.ErrScheduledArticleNotFound
	}

	hasContent, mc, err := s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get master context: %w", err)
	}
	if !hasContent {
		return nil, scheduledArticleService.ErrArticleNotGenerated
	}

	// Inline images share the user-uploaded namespace but carry an `inline`
	// discriminator instead of a position; the unix-nano suffix keeps repeated
	// inserts from colliding.
	key := fmt.Sprintf("%s-inline-%d.%s", userUploadedArticlePrefix(userId, mc.ID.Hex()), time.Now().UnixNano(), ext)

	presigned, err := s.s3.GenerateSinglepartUploadPresignedURLs(ctx, s.s3Bucket, []idto.PresignedSinglepartPutRequest{
		{Key: key, ContentType: contentType},
	})
	if err != nil {
		return nil, fmt.Errorf("presign inline image upload: %w", err)
	}
	if len(presigned) != 1 {
		return nil, fmt.Errorf("presign inline image upload: expected 1 url, got %d", len(presigned))
	}

	return &scheduledArticleService.ImageUploadTarget{
		Key:       presigned[0].Key,
		UploadURL: presigned[0].URL,
		Headers:   presigned[0].Headers,
		PublicURL: s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, key),
	}, nil
}

// userUploadedArticlePrefix builds the per-user/article key prefix under the
// user-uploaded namespace, shared by every image the user uploaded for this
// article regardless of slot (thumbnail, mid-article, or inline). SaveDraft
// validates a client-supplied slot key against this prefix so any of the user's
// own uploads for the article can be promoted into a slot — e.g. a hero image
// dropped above the title becoming the thumbnail.
func userUploadedArticlePrefix(userId, masterContextID string) string {
	return fmt.Sprintf("%s%s-%s", userUploadedPrefix, userId, masterContextID)
}

// userUploadedImageKeyPrefix builds the per-user/article/position key prefix
// under the user-uploaded namespace. The actual key appends a unix-nano suffix
// and extension.
func userUploadedImageKeyPrefix(userId, masterContextID, position string) string {
	return fmt.Sprintf("%s-%s", userUploadedArticlePrefix(userId, masterContextID), position)
}

// SaveDraft persists a partial edit onto the master context for the given
// scheduled article and flips its status to `draft`. The two doc updates are
// not transactional — a crash between them is recoverable: the read path treats
// "edited block present" as equivalent to "status is draft".
//
// Image edits are applied as part of the same master-context write: alt text is
// updated in place, and a replaced image swaps its stored S3 key and has the
// body's image URL rewritten to match. The *new* data is persisted first; only
// then are the now-orphaned previous S3 objects deleted (best-effort), so a
// delete failure can never lose the user's new image.
func (s *svc) SaveDraft(ctx context.Context, userId, scheduledArticleID string, payload scheduledArticleService.SaveDraftPayload) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return scheduledArticleService.ErrScheduledArticleNotFound
	}

	hasContent, mc, err := s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get master context: %w", err)
	}
	if !hasContent {
		return scheduledArticleService.ErrArticleNotGenerated
	}

	// Apply per-position image edits onto a copy of the stored images, rewriting
	// the article body URL for any replacement. `orphanedKeys` are the previous
	// S3 objects to delete once the new pointers are durably persisted. The image
	// URL rewrite runs on the still-merged body so a replaced thumbnail's URL is
	// updated before it is stripped out below.
	articleContent := payload.ArticleContent
	updatedImages, orphanedKeys, err := s.applyImageEdits(userId, mc, payload.Images, &articleContent)
	if err != nil {
		return err
	}

	// The review UI edits (and returns) the merged document — title as the H1,
	// thumbnail as the lead image. Split them back out so the stored body stays
	// title/thumbnail-free; the extracted H1 becomes the article title (below),
	// the thumbnail is carried by the structured Images edits.
	extractedTitle, cleanBody := models.StripArticleTitleAndThumbnail(articleContent)

	edited := &models.CGEEdited{
		ArticleContent:  cleanBody,
		MetaTitle:       payload.MetaTitle,
		MetaDescription: payload.MetaDescription,
		URLSlug:         payload.URLSlug,
	}

	wordCount := models.CountArticleWords(cleanBody)
	wemcUpdate := models.WEMCUpdateReq{
		Edited:    edited,
		WordCount: &wordCount,
	}
	if updatedImages != nil {
		wemcUpdate.Images = updatedImages
	}
	if err := s.store.UpdateWebEntityMasterContext(ctx, mc.ID.Hex(), wemcUpdate); err != nil {
		return fmt.Errorf("save edited content: %w", err)
	}

	// New image pointers are now persisted — safe to drop the replaced objects.
	// Best-effort: a delete failure leaves an orphan in S3 but must not fail the
	// save or unwind the new data the user just committed.
	for _, key := range orphanedKeys {
		if delErr := s.s3.DeleteFile(ctx, s.s3Bucket, key); delErr != nil {
			log.Warn("save draft: failed to delete replaced image",
				"key", key, "scheduledArticleId", scheduledArticleID, "error", delErr)
		}
	}

	// The save commits everything the editor holds — spliced rewrites ride the
	// body, regenerated images ride the images edits — so the pending-regen
	// set is settled. Best-effort: a failed clear leaves stale pending entries
	// whose old passages no longer exist; the client drops (and discards)
	// those on the next load.
	if err := s.store.ClearPendingRegen(ctx, mc.ID.Hex()); err != nil {
		log.Warn("save draft: failed to clear pending regens",
			"scheduledArticleId", scheduledArticleID, "error", err)
	}

	// Title and status both live on the ScheduledArticle doc — fold them into
	// one update so a draft save with a renamed title is a single round-trip. The
	// title comes from an explicit payload field when the client sends one, else
	// from the H1 extracted out of the edited body; an empty result leaves the
	// stored title untouched (a titleless article would break the calendar list).
	status := models.ScheduledArticleStatusDraft
	saUpdate := models.ScheduledArticleUpdateReq{Status: &status}
	title := payload.Title
	if title == "" {
		title = extractedTitle
	}
	if title != "" {
		saUpdate.Title = &title
	}
	if err := s.store.UpdateScheduledArticle(ctx, scheduledArticleID, saUpdate); err != nil {
		return fmt.Errorf("update scheduled article on draft save: %w", err)
	}

	return nil
}

// DiscardPendingRegen implements the regen undo — see the interface doc.
func (s *svc) DiscardPendingRegen(ctx context.Context, userId, scheduledArticleID string, sectionID *int64, imagePosition *string) error {
	if (sectionID == nil) == (imagePosition == nil) {
		return scheduledArticleService.ErrInvalidImagePosition
	}

	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return scheduledArticleService.ErrScheduledArticleNotFound
	}

	hasContent, mc, err := s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
	if err != nil {
		return fmt.Errorf("get master context: %w", err)
	}
	if !hasContent {
		return scheduledArticleService.ErrArticleNotGenerated
	}

	if sectionID != nil {
		if err := s.store.RemovePendingSectionRegen(ctx, mc.ID.Hex(), *sectionID); err != nil {
			return fmt.Errorf("remove pending section regen: %w", err)
		}
		return nil
	}

	position := *imagePosition
	if _, ok := validImagePositions[position]; !ok {
		return scheduledArticleService.ErrInvalidImagePosition
	}
	removed, err := s.store.RemovePendingImageRegen(ctx, mc.ID.Hex(), position)
	if err != nil {
		return fmt.Errorf("remove pending image regen: %w", err)
	}
	// The atomic remove returned the entry exactly once, so deleting its
	// object can't race a save that committed it (a commit clears the pending
	// set first, making this a no-op). Guard against the stored image anyway,
	// and never delete outside the regen namespace.
	if removed != nil && removed.S3Key != "" &&
		strings.HasPrefix(removed.S3Key, models.RegeneratedImageKeyPrefix(userId, mc.ID.Hex())) {
		for _, img := range mc.Images {
			if img.S3Key == removed.S3Key {
				return nil
			}
		}
		if delErr := s.s3.DeleteFile(ctx, s.s3Bucket, removed.S3Key); delErr != nil {
			log.Warn("discard pending regen: failed to delete pending image object",
				"key", removed.S3Key, "scheduledArticleId", scheduledArticleID, "error", delErr)
		}
	}
	return nil
}

// applyImageEdits folds the per-position image edits from a save-draft payload
// onto the master context's stored images. It returns the full updated image
// slice to persist (nil when there were no image edits, so the stored array is
// left untouched) and the list of previous S3 keys that were replaced and
// should be deleted after the new state is saved. When an image is replaced,
// the old image URL in *articleContent is rewritten to the new one so the body
// and the structured image record stay in sync.
//
// Replacement keys are validated against the user-uploaded namespace for this
// user + master context + position, so a client can't repoint a stored image
// at an arbitrary object.
func (s *svc) applyImageEdits(userId string, mc *models.WebEntityMasterContext, edits []scheduledArticleService.SaveDraftImage, articleContent *string) ([]models.CGEImage, []string, error) {
	if len(edits) == 0 {
		return nil, nil, nil
	}

	// Index the stored images by position for in-place edits.
	byPosition := make(map[string]int, len(mc.Images))
	for i, img := range mc.Images {
		byPosition[img.Position] = i
	}

	images := make([]models.CGEImage, len(mc.Images))
	copy(images, mc.Images)

	var orphanedKeys []string
	for _, edit := range edits {
		if _, ok := validImagePositions[edit.Position]; !ok {
			return nil, nil, scheduledArticleService.ErrInvalidImagePosition
		}
		idx, ok := byPosition[edit.Position]
		if !ok {
			// No stored slot for this position — nothing to attach the edit to.
			return nil, nil, scheduledArticleService.ErrInvalidImagePosition
		}

		images[idx].Alt = edit.Alt

		newKey := strings.TrimSpace(edit.NewS3Key)
		if newKey == "" {
			continue // alt-only edit
		}

		// Accept any key in this user's user-uploaded namespace for this article,
		// not just the slot's own prefix: an image uploaded inline (or for another
		// slot) can be promoted into this slot. Server-side regenerations live in
		// their own versioned namespace (models.RegeneratedImageKeyPrefix) and are
		// committed through the same swap, so that prefix is accepted too. Keys
		// outside both namespaces are rejected, so a client can't point a slot at
		// an arbitrary object.
		uploadedPrefix := userUploadedArticlePrefix(userId, mc.ID.Hex()) + "-"
		regenPrefix := models.RegeneratedImageKeyPrefix(userId, mc.ID.Hex()) + "-"
		if !strings.HasPrefix(newKey, uploadedPrefix) && !strings.HasPrefix(newKey, regenPrefix) {
			return nil, nil, scheduledArticleService.ErrInvalidImageKey
		}

		oldKey := images[idx].S3Key
		if oldKey == newKey {
			continue // idempotent re-save of the same uploaded key
		}

		// Rewrite the body image URL old → new so the rendered article matches
		// the structured record. The frontend usually already embeds the new
		// URL; this also covers the case where it didn't.
		if oldKey != "" && articleContent != nil {
			oldURL := s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, oldKey)
			newURL := s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, newKey)
			*articleContent = strings.ReplaceAll(*articleContent, oldURL, newURL)
			orphanedKeys = append(orphanedKeys, oldKey)
		}

		images[idx].S3Key = newKey
	}

	return images, orphanedKeys, nil
}

// rawJSONToAny decodes a stored json.RawMessage so the API serialises the
// nested schema as a JSON object rather than a base64 string.
func rawJSONToAny(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// currentWeekRange returns [Monday 00:00, next Monday 00:00) in UTC for the
// week containing `now`.
func currentWeekRange(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	// time.Weekday: Sunday=0 .. Saturday=6. Days elapsed since Monday.
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -daysSinceMonday)
	return start, start.AddDate(0, 0, 7)
}
