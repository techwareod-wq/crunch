package scheduledarticles

import (
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	contentGenerationEngine "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine"
)

const dateLayout = "2006-01-02"

// for a web entity. `webEntityId` is required. `from`/`to` are optional
// YYYY-MM-DD dates (inclusive) — when omitted, the current week is returned.
// They must be supplied together or not at all.
func HandleGetScheduledArticles(w http.ResponseWriter, r *http.Request) {
	webEntityId := r.URL.Query().Get("webEntityId")
	if webEntityId == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if (fromStr == "") != (toStr == "") {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	var from, to *time.Time
	if fromStr != "" {
		f, err := time.Parse(dateLayout, fromStr)
		if err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		t, err := time.Parse(dateLayout, toStr)
		if err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		if t.Before(f) {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		from, to = &f, &t
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	// `view=status` returns the compact polling payload (see
	// dto.ScheduledArticleStatus): only the fields that change while a row
	// settles, and no keyword hydration. Any other value falls through to the
	// full calendar payload so existing consumers are unaffected.
	if r.URL.Query().Get("view") == "status" {
		resp, err := appCtx.InternalServices.ScheduledArticleService.GetScheduledArticleStatuses(ctx, userId, webEntityId, from, to)
		if err != nil {
			if errors.Is(err, scheduledArticleService.ErrWebEntityNotFound) {
				middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
				return
			}
			logger.Error("Failed to get scheduled article statuses", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
			return
		}
		middleware.SendJSONResponse(w, r, http.StatusOK, resp)
		return
	}

	resp, err := appCtx.InternalServices.ScheduledArticleService.GetScheduledArticles(ctx, userId, webEntityId, from, to)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrWebEntityNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
			return
		}
		logger.Error("Failed to get scheduled articles", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// All edit fields are taken as-is — empty strings overwrite the pipeline
// values, so the client must send the full intended state of each field.
type saveDraftRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	Title              string `json:"title"`
	ArticleContent     string `json:"articleContent"`
	MetaTitle          string `json:"metaTitle"`
	MetaDescription    string `json:"metaDescription"`
	URLSlug            string `json:"urlSlug"`
	// Images carries per-position alt-text edits and, for replaced images, the
	// S3 key the client uploaded via a presigned URL (see image-upload-url).
	// Omit a position to leave its stored image untouched.
	Images []saveDraftImage `json:"images,omitempty"`
}

type saveDraftImage struct {
	Position string `json:"position"`
	Alt      string `json:"alt"`
	// NewS3Key is set only when the user replaced the image; empty for an
	// alt-only edit.
	NewS3Key string `json:"newS3Key,omitempty"`
}

func HandleSaveArticleDraft(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(saveDraftRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	images := make([]scheduledArticleService.SaveDraftImage, len(req.Images))
	for i, img := range req.Images {
		images[i] = scheduledArticleService.SaveDraftImage{
			Position: img.Position,
			Alt:      img.Alt,
			NewS3Key: img.NewS3Key,
		}
	}

	err := appCtx.InternalServices.ScheduledArticleService.SaveDraft(ctx, userId, req.ScheduledArticleID, scheduledArticleService.SaveDraftPayload{
		Title:           req.Title,
		ArticleContent:  req.ArticleContent,
		MetaTitle:       req.MetaTitle,
		MetaDescription: req.MetaDescription,
		URLSlug:         req.URLSlug,
		Images:          images,
	})
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrArticleNotGenerated),
			errors.Is(err, scheduledArticleService.ErrInvalidImagePosition),
			errors.Is(err, scheduledArticleService.ErrInvalidImageKey):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to save article draft", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "draft"})
}

// imageUploadURLRequest asks for a presigned PUT to upload a replacement image
// for a given article + image position. The client uploads the bytes straight
// to S3 with the returned URL, then echoes the returned key back on the next
// save-draft call.
type imageUploadURLRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	Position           string `json:"position"`
	ContentType        string `json:"contentType"`
}

// HandleCreateImageUploadURL issues the presigned PUT + public URL for a
// user-uploaded replacement image. Ownership and input validation live in the
// service; the handler only maps HTTP ⇄ service.
func HandleCreateImageUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(imageUploadURLRequest)
	if !ok || req.ScheduledArticleID == "" || req.Position == "" || req.ContentType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	target, err := appCtx.InternalServices.ScheduledArticleService.CreateImageUploadURL(ctx, userId, req.ScheduledArticleID, req.Position, req.ContentType)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrArticleNotGenerated),
			errors.Is(err, scheduledArticleService.ErrInvalidImagePosition),
			errors.Is(err, scheduledArticleService.ErrInvalidImageContentType):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to create image upload url", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, target)
}

// inlineImageUploadURLRequest asks for a presigned PUT to upload an image the
// user inserts inline into the article body via the editor toolbar. Unlike the
// position-scoped replacement upload there is no slot — the returned public URL
// is embedded straight into the saved article content.
type inlineImageUploadURLRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	ContentType        string `json:"contentType"`
}

// HandleCreateInlineImageUploadURL issues the presigned PUT + public URL for a
// user-inserted inline image. Ownership and input validation live in the
// service; the handler only maps HTTP ⇄ service.
func HandleCreateInlineImageUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(inlineImageUploadURLRequest)
	if !ok || req.ScheduledArticleID == "" || req.ContentType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	target, err := appCtx.InternalServices.ScheduledArticleService.CreateInlineImageUploadURL(ctx, userId, req.ScheduledArticleID, req.ContentType)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrArticleNotGenerated),
			errors.Is(err, scheduledArticleService.ErrInvalidImageContentType):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to create inline image upload url", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, target)
}

// updateScheduledArticleRequest is the dashboard sidebar / drag-drop edit
// payload. Every field except scheduledArticleId is optional — pointer
// semantics let the service distinguish "leave alone" from "clear". Title is
// trimmed and rejected when empty; ArticleType is validated against the
// canonical set; ScheduleDate is `YYYY-MM-DD` and interpreted as UTC midnight.
type updateScheduledArticleRequest struct {
	ScheduledArticleID string  `json:"scheduledArticleId"`
	Title              *string `json:"title,omitempty"`
	ArticleType        *string `json:"articleType,omitempty"`
	// Reasoning is the retitle-preview reasoning the client carries back so a
	// type change commits the new title + reasoning together without a second
	// LLM call. Only honoured alongside an article-type change.
	Reasoning              *string `json:"reasoning,omitempty"`
	AdditionalInstructions *string `json:"additionalInstructions,omitempty"`
	ScheduleDate           *string `json:"scheduleDate,omitempty"`
	// InternalLinkingEnabled toggles the per-article internal-link insertion
	// step. Omitted leaves the persisted value untouched.
	InternalLinkingEnabled *bool `json:"internalLinkingEnabled,omitempty"`
	// ThumbnailStyle overrides the per-article thumbnail style. Omitted leaves
	// the persisted value untouched; "" resets to inherit the WebEntity default;
	// a non-empty value must be a known catalog style (else 400).
	ThumbnailStyle *string `json:"thumbnailStyle,omitempty"`
}

// HandleUpdateScheduledArticle applies a partial dashboard edit (sidebar form
// or drag-drop reschedule) to a calendar slot. Status-based gating lives in
// the service; the handler only translates HTTP → service payload and
// service errors → HTTP responses.
func HandleUpdateScheduledArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(updateScheduledArticleRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	payload := scheduledArticleService.DashboardEditPayload{
		Title:                  req.Title,
		ArticleType:            req.ArticleType,
		Reasoning:              req.Reasoning,
		AdditionalInstructions: req.AdditionalInstructions,
		InternalLinkingEnabled: req.InternalLinkingEnabled,
		ThumbnailStyle:         req.ThumbnailStyle,
	}
	if req.ScheduleDate != nil {
		parsed, err := time.Parse(dateLayout, *req.ScheduleDate)
		if err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidScheduleDate)
			return
		}
		payload.ScheduleDate = &parsed
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.UpdateFromDashboard(ctx, userId, req.ScheduledArticleID, payload)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotEditable),
			errors.Is(err, scheduledArticleService.ErrFieldNotEditableForStatus):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotEditable)
		case errors.Is(err, scheduledArticleService.ErrInvalidScheduleDate):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidScheduleDate)
		case errors.Is(err, scheduledArticleService.ErrInvalidArticleType):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidArticleType)
		case errors.Is(err, scheduledArticleService.ErrInvalidThumbnailStyle):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to update scheduled article", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// It carries the user's "New article" selection: which keyword (by id, verified
// server-side against the user's web entity context), which article type, and
// the publish date. The title is generated asynchronously, so the created slot
// starts in the transient `scheduling` status.
type scheduleArticleRequest struct {
	WebEntityID  string `json:"webEntityId"`
	KeywordID    string `json:"keywordId"`
	ArticleType  string `json:"articleType"`
	ScheduleDate string `json:"scheduleDate"` // YYYY-MM-DD, interpreted as UTC midnight.
}

// dispatches its title-generation step. Responds 202 with the hydrated slot
// (status `scheduling`) so the dashboard can render the row immediately; the
// row flips to `scheduled` on the next calendar fetch once the title lands.
func HandleScheduleArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(scheduleArticleRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityID == "" || req.KeywordID == "" || req.ArticleType == "" || req.ScheduleDate == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	scheduleDate, err := time.Parse(dateLayout, req.ScheduleDate)
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidScheduleDate)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	sa, kw, err := appCtx.InternalServices.SchedulingService.ScheduleArticle(ctx, userId, schedulingEngine.ScheduleArticleParams{
		WebEntityID:  req.WebEntityID,
		KeywordID:    req.KeywordID,
		ArticleType:  models.ArticleType(req.ArticleType),
		ScheduleDate: scheduleDate,
	})
	if err != nil {
		switch {
		case errors.Is(err, schedulingEngine.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, schedulingEngine.ErrKeywordNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		case errors.Is(err, schedulingEngine.ErrInvalidArticleType):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidArticleType)
		case errors.Is(err, schedulingEngine.ErrInvalidScheduleDate):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidScheduleDate)
		default:
			logger.Error("Failed to schedule article", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	var out dto.ScheduledArticle
	out.FromDbModel(sa, kw)
	middleware.SendJSONResponse(w, r, http.StatusAccepted, out)
}

// for the dashboard "Regenerate suggestions" control. Generate-once on the
// backend: the first call generates + persists, later calls reuse the set.
type suggestTitlesRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
}

func HandleSuggestTitles(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(suggestTitlesRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	titles, err := appCtx.InternalServices.SchedulingService.GenerateTitleSuggestions(ctx, userId, req.ScheduledArticleID)
	if err != nil {
		switch {
		case errors.Is(err, schedulingEngine.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, schedulingEngine.ErrScheduledArticleNotEditable):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotEditable)
		default:
			logger.Error("Failed to generate title suggestions", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrLLMProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string][]string{"titles": titles})
}

// returns a proposed title + reasoning WITHOUT persisting — the dashboard
// fills the title field and only commits on save.
type retitlePreviewRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	ArticleType        string `json:"articleType"`
}

func HandleRetitlePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(retitlePreviewRequest)
	if !ok || req.ScheduledArticleID == "" || req.ArticleType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	title, reasoning, err := appCtx.InternalServices.SchedulingService.PreviewRetitleForType(ctx, userId, req.ScheduledArticleID, req.ArticleType)
	if err != nil {
		switch {
		case errors.Is(err, schedulingEngine.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, schedulingEngine.ErrScheduledArticleNotEditable):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotEditable)
		case errors.Is(err, schedulingEngine.ErrInvalidArticleType):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidArticleType)
		default:
			logger.Error("Failed to generate retitle preview", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrLLMProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{
		"title":     title,
		"reasoning": reasoning,
	})
}

// regenerateSectionRequest carries a user-highlighted passage of the article
// plus an optional rewrite request. The article body is never mutated: the
// rewrite is recorded as a pending entry (so it survives reloads until the
// user saves or discards it) and returned for the client to splice into the
// editor; the normal draft-save commits it.
type regenerateSectionRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	SelectedMarkdown   string `json:"selectedMarkdown"`
	Instructions       string `json:"instructions,omitempty"`
	// Label is the display name for the pending entry ("Selection" or the
	// section heading). Stored, never prompted.
	Label string `json:"label,omitempty"`
}

func HandleRegenerateSection(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(regenerateSectionRequest)
	if !ok || req.ScheduledArticleID == "" || req.SelectedMarkdown == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	out, err := appCtx.InternalServices.ContentGenerationService.RegenerateSection(ctx, userId, contentGenerationEngine.RegenerateSectionRequest{
		ScheduledArticleID: req.ScheduledArticleID,
		SelectedMarkdown:   req.SelectedMarkdown,
		Instructions:       req.Instructions,
		Label:              req.Label,
	})
	if err != nil {
		switch {
		case errors.Is(err, contentGenerationEngine.ErrRegenArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, contentGenerationEngine.ErrRegenNotGenerated),
			errors.Is(err, contentGenerationEngine.ErrRegenInvalidInput):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to regenerate section", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrLLMProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"id":                  out.ID,
		"regeneratedMarkdown": out.Markdown,
	})
}

// discardRegenRequest is the regen undo: exactly one of sectionId /
// imagePosition names the pending entry to drop. Discarding an already-gone
// entry is a no-op (it may have been settled from another tab).
type discardRegenRequest struct {
	ScheduledArticleID string  `json:"scheduledArticleId"`
	SectionID          *int64  `json:"sectionId,omitempty"`
	ImagePosition      *string `json:"imagePosition,omitempty"`
}

func HandleDiscardRegen(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(discardRegenRequest)
	if !ok || req.ScheduledArticleID == "" || (req.SectionID == nil) == (req.ImagePosition == nil) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.DiscardPendingRegen(ctx, userId, req.ScheduledArticleID, req.SectionID, req.ImagePosition)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrArticleNotGenerated),
			errors.Is(err, scheduledArticleService.ErrInvalidImagePosition):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to discard pending regen", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "discarded"})
}

// regenerateImageRequest asks for a fresh generation of one image slot. The
// new object lands on a versioned key that is only committed when the client
// echoes it back on the next save-draft (images[].newS3Key) — until then the
// current image is untouched, which is what makes the client-side undo work.
type regenerateImageRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	Position           string `json:"position"`
	Instructions       string `json:"instructions,omitempty"`
}

func HandleRegenerateImage(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(regenerateImageRequest)
	if !ok || req.ScheduledArticleID == "" || req.Position == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	img, err := appCtx.InternalServices.ContentGenerationService.RegenerateImage(ctx, userId, contentGenerationEngine.RegenerateImageRequest{
		ScheduledArticleID: req.ScheduledArticleID,
		Position:           req.Position,
		Instructions:       req.Instructions,
	})
	if err != nil {
		switch {
		case errors.Is(err, contentGenerationEngine.ErrRegenArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, contentGenerationEngine.ErrRegenNotGenerated),
			errors.Is(err, contentGenerationEngine.ErrRegenInvalidInput):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		default:
			logger.Error("Failed to regenerate image", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrLLMProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{
		"newS3Key": img.S3Key,
		"url":      img.URL,
		"alt":      img.Alt,
	})
}

// setPublishStateRequest is the per-article live/draft override toggle. Sent by
// both publish-state sidebar controls; persists an explicit override so an
// untouched article keeps tracking the WebEntity generic default.
type setPublishStateRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	PublishAsLive      bool   `json:"publishAsLive"`
}

// HandleSetPublishState persists the per-article live/draft override. Editable
// in every state except mid-generation (enforced by the service). Ownership and
// state gating live in the service; the handler only maps HTTP ⇄ service.
func HandleSetPublishState(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(setPublishStateRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.SetPublishState(ctx, userId, req.ScheduledArticleID, req.PublishAsLive)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotEditable):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotEditable)
		default:
			logger.Error("Failed to set publish state", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func HandleDeleteScheduledArticle(w http.ResponseWriter, r *http.Request) {
	scheduledArticleID := r.URL.Query().Get("scheduledArticleId")
	if scheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.DeleteScheduledArticle(ctx, userId, scheduledArticleID)
	if err != nil {
		switch {
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		case errors.Is(err, scheduledArticleService.ErrScheduledArticleNotEditable):
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotEditable)
		default:
			logger.Error("Failed to delete scheduled article", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "deleted"})
}

func HandleGetArticleBySchedule(w http.ResponseWriter, r *http.Request) {
	scheduledArticleID := r.URL.Query().Get("scheduledArticleId")
	if scheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.ScheduledArticleService.GetArticleByScheduleID(ctx, userId, scheduledArticleID)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
			return
		}
		logger.Error("Failed to get article by schedule id", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrScheduledArticlesRetrievalFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
