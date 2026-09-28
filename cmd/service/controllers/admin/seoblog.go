package admin

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	contentGenerationEngine "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
)

type adminOrchestrateRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
	ArticleType        string `json:"articleType,omitempty"`
	ProposedTitle      string `json:"proposedTitle,omitempty"`
}

type adminRetryRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
}

// HandleAdminOrchestrate mirrors POST /v1/seo-blog/orchestrate for a target
// user — full article generation on their behalf.
func HandleAdminOrchestrate(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminOrchestrateRequest)
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
	ctx := r.Context()
	logger := middleware.GetLogger(r)
	targetId := target.ID.Hex()

	resolved, err := appCtx.InternalServices.ScheduledArticleService.ResolveOrchestrateContext(ctx, targetId, req.ScheduledArticleID)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
			return
		}
		logger.Error("Failed to resolve orchestrate context", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	articleType := req.ArticleType
	if articleType == "" {
		articleType = resolved.ArticleType
	}
	proposedTitle := req.ProposedTitle
	if proposedTitle == "" {
		proposedTitle = resolved.Title
	}

	err = appCtx.InternalServices.ContentGenerationService.Orchestrate(ctx, targetId, contentGenerationEngine.CGEOrchestratePayload{
		ScheduledArticleID:     req.ScheduledArticleID,
		WebEntityContextID:     resolved.WebEntityContextID,
		KeywordID:              resolved.KeywordID,
		ArticleType:            articleType,
		ProposedTitle:          proposedTitle,
		AdditionalInstructions: resolved.AdditionalInstructions,
		InternalLinkingEnabled: resolved.InternalLinkingEnabled,
		ThumbnailStyle:         resolved.ThumbnailStyle,
	})
	if err != nil {
		logger.Error("Failed to orchestrate content generation", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

// HandleAdminRetry mirrors POST /v1/seo-blog/retry for a target user — the
// headline ops use case: re-run a user's failed generation with whatever the
// slot currently holds. Works for lapsed-subscription targets because admin
// routes skip the subscription gate.
func HandleAdminRetry(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminRetryRequest)
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
	ctx := r.Context()
	logger := middleware.GetLogger(r)
	targetId := target.ID.Hex()

	resolved, err := appCtx.InternalServices.ScheduledArticleService.ResolveOrchestrateContext(ctx, targetId, req.ScheduledArticleID)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
			return
		}
		logger.Error("Failed to resolve orchestrate context for retry", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	err = appCtx.InternalServices.ContentGenerationService.Orchestrate(ctx, targetId, contentGenerationEngine.CGEOrchestratePayload{
		ScheduledArticleID:     req.ScheduledArticleID,
		WebEntityContextID:     resolved.WebEntityContextID,
		KeywordID:              resolved.KeywordID,
		ArticleType:            resolved.ArticleType,
		ProposedTitle:          resolved.Title,
		AdditionalInstructions: resolved.AdditionalInstructions,
		InternalLinkingEnabled: resolved.InternalLinkingEnabled,
		ThumbnailStyle:         resolved.ThumbnailStyle,
	})
	if err != nil {
		logger.Error("Failed to retry content generation", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}
