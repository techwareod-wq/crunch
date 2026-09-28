package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine"
)

const dateLayout = "2006-01-02"

// HandleAdminGetScheduledArticles mirrors GET /v1/scheduled-articles for a
// target user (?userId= plus the existing webEntityId/from/to/view params).
func HandleAdminGetScheduledArticles(w http.ResponseWriter, r *http.Request) {
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

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)
	targetId := target.ID.Hex()

	if r.URL.Query().Get("view") == "status" {
		resp, err := appCtx.InternalServices.ScheduledArticleService.GetScheduledArticleStatuses(ctx, targetId, webEntityId, from, to)
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

	resp, err := appCtx.InternalServices.ScheduledArticleService.GetScheduledArticles(ctx, targetId, webEntityId, from, to)
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

// HandleAdminGetArticleBySchedule mirrors GET /v1/scheduled-articles/article
// for a target user (?userId=&scheduledArticleId=).
func HandleAdminGetArticleBySchedule(w http.ResponseWriter, r *http.Request) {
	scheduledArticleID := r.URL.Query().Get("scheduledArticleId")
	if scheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.ScheduledArticleService.GetArticleByScheduleID(r.Context(), target.ID.Hex(), scheduledArticleID)
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

type adminScheduleArticleRequest struct {
	UserID       string `json:"userId"`
	WebEntityID  string `json:"webEntityId"`
	KeywordID    string `json:"keywordId"`
	ArticleType  string `json:"articleType"`
	ScheduleDate string `json:"scheduleDate"` // YYYY-MM-DD, interpreted as UTC midnight.
}

// HandleAdminScheduleArticle mirrors POST /v1/scheduled-articles/schedule for
// a target user (async title generation spend).
func HandleAdminScheduleArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminScheduleArticleRequest)
	if !ok || req.WebEntityID == "" || req.KeywordID == "" || req.ArticleType == "" || req.ScheduleDate == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	scheduleDate, err := time.Parse(dateLayout, req.ScheduleDate)
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidScheduleDate)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	sa, kw, err := appCtx.InternalServices.SchedulingService.ScheduleArticle(r.Context(), target.ID.Hex(), schedulingEngine.ScheduleArticleParams{
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

// HandleAdminDeleteScheduledArticle mirrors DELETE
// /v1/scheduled-articles/article/delete for a target user
// (?userId=&scheduledArticleId=). Destructive: removes a calendar slot.
func HandleAdminDeleteScheduledArticle(w http.ResponseWriter, r *http.Request) {
	scheduledArticleID := r.URL.Query().Get("scheduledArticleId")
	if scheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.DeleteScheduledArticle(r.Context(), target.ID.Hex(), scheduledArticleID)
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

// Same full-state overwrite semantics as the user route: every field is taken
// as-is, so the admin must send the complete intended state of each field.
type adminSaveDraftRequest struct {
	UserID             string                `json:"userId"`
	ScheduledArticleID string                `json:"scheduledArticleId"`
	Title              string                `json:"title"`
	ArticleContent     string                `json:"articleContent"`
	MetaTitle          string                `json:"metaTitle"`
	MetaDescription    string                `json:"metaDescription"`
	URLSlug            string                `json:"urlSlug"`
	Images             []adminSaveDraftImage `json:"images,omitempty"`
}

type adminSaveDraftImage struct {
	Position string `json:"position"`
	Alt      string `json:"alt"`
	NewS3Key string `json:"newS3Key,omitempty"`
}

// HandleAdminSaveArticleDraft mirrors PATCH /v1/scheduled-articles/article/draft
// for a target user.
func HandleAdminSaveArticleDraft(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSaveDraftRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	images := make([]scheduledArticleService.SaveDraftImage, len(req.Images))
	for i, img := range req.Images {
		images[i] = scheduledArticleService.SaveDraftImage{
			Position: img.Position,
			Alt:      img.Alt,
			NewS3Key: img.NewS3Key,
		}
	}

	err := appCtx.InternalServices.ScheduledArticleService.SaveDraft(r.Context(), target.ID.Hex(), req.ScheduledArticleID, scheduledArticleService.SaveDraftPayload{
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

type adminImageUploadURLRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
	Position           string `json:"position"`
	ContentType        string `json:"contentType"`
}

// HandleAdminCreateImageUploadURL mirrors POST
// /v1/scheduled-articles/article/image-upload-url for a target user — the
// presigned PUT writes into the target's S3 space.
func HandleAdminCreateImageUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminImageUploadURLRequest)
	if !ok || req.ScheduledArticleID == "" || req.Position == "" || req.ContentType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	uploadTarget, err := appCtx.InternalServices.ScheduledArticleService.CreateImageUploadURL(r.Context(), target.ID.Hex(), req.ScheduledArticleID, req.Position, req.ContentType)
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

	middleware.SendJSONResponse(w, r, http.StatusOK, uploadTarget)
}

type adminInlineImageUploadURLRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
	ContentType        string `json:"contentType"`
}

// HandleAdminCreateInlineImageUploadURL mirrors POST
// /v1/scheduled-articles/article/inline-image-upload-url for a target user.
func HandleAdminCreateInlineImageUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminInlineImageUploadURLRequest)
	if !ok || req.ScheduledArticleID == "" || req.ContentType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	uploadTarget, err := appCtx.InternalServices.ScheduledArticleService.CreateInlineImageUploadURL(r.Context(), target.ID.Hex(), req.ScheduledArticleID, req.ContentType)
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

	middleware.SendJSONResponse(w, r, http.StatusOK, uploadTarget)
}

type adminSuggestTitlesRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
}

// HandleAdminSuggestTitles mirrors POST
// /v1/scheduled-articles/article/suggest-titles for a target user (LLM spend).
func HandleAdminSuggestTitles(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSuggestTitlesRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	titles, err := appCtx.InternalServices.SchedulingService.GenerateTitleSuggestions(r.Context(), target.ID.Hex(), req.ScheduledArticleID)
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

type adminRetitlePreviewRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
	ArticleType        string `json:"articleType"`
}

// HandleAdminRetitlePreview mirrors POST
// /v1/scheduled-articles/article/retitle-preview for a target user (LLM spend).
func HandleAdminRetitlePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminRetitlePreviewRequest)
	if !ok || req.ScheduledArticleID == "" || req.ArticleType == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	title, reasoning, err := appCtx.InternalServices.SchedulingService.PreviewRetitleForType(r.Context(), target.ID.Hex(), req.ScheduledArticleID, req.ArticleType)
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

// Same pointer semantics as the user route: omitted fields are left alone.
type adminUpdateScheduledArticleRequest struct {
	UserID                 string  `json:"userId"`
	ScheduledArticleID     string  `json:"scheduledArticleId"`
	Title                  *string `json:"title,omitempty"`
	ArticleType            *string `json:"articleType,omitempty"`
	Reasoning              *string `json:"reasoning,omitempty"`
	AdditionalInstructions *string `json:"additionalInstructions,omitempty"`
	ScheduleDate           *string `json:"scheduleDate,omitempty"`
	InternalLinkingEnabled *bool   `json:"internalLinkingEnabled,omitempty"`
	ThumbnailStyle         *string `json:"thumbnailStyle,omitempty"`
}

// HandleAdminUpdateScheduledArticle mirrors PATCH
// /v1/scheduled-articles/article/edit for a target user. The service's
// status-based gating still applies.
func HandleAdminUpdateScheduledArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminUpdateScheduledArticleRequest)
	if !ok || req.ScheduledArticleID == "" {
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

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.UpdateFromDashboard(r.Context(), target.ID.Hex(), req.ScheduledArticleID, payload)
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

type adminSetPublishStateRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
	PublishAsLive      bool   `json:"publishAsLive"`
}

// HandleAdminSetPublishState mirrors POST
// /v1/scheduled-articles/article/publish-state for a target user —
// outward-facing: flips live/draft on the target's site.
func HandleAdminSetPublishState(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSetPublishStateRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.ScheduledArticleService.SetPublishState(r.Context(), target.ID.Hex(), req.ScheduledArticleID, req.PublishAsLive)
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
