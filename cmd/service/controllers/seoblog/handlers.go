package seoblog

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	contentGenerationEngine "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
)

// orchestrateRequest is the manual-trigger payload for CGE. Only
// ScheduledArticleID is required — keyword id and web-entity-context id are
// resolved from the scheduled article doc server-side so the client can't
// fire generation against keywords it doesn't own.
type orchestrateRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	ArticleType        string `json:"articleType,omitempty"`
	ProposedTitle      string `json:"proposedTitle,omitempty"`
}

func HandleOrchestrate(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(orchestrateRequest)
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

	resolved, err := appCtx.InternalServices.ScheduledArticleService.ResolveOrchestrateContext(ctx, userId, req.ScheduledArticleID)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
			return
		}
		logger.Error("Failed to resolve orchestrate context", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	// Free-plan article cap: at most trial.maxArticles generations ever
	// STARTED per trial user, read from the monotonic lifetime counter —
	// deleting articles does not refund slots. Only a slot that has never
	// generated consumes the cap, so re-triggering an in-flight/errored slot —
	// and the dedicated /retry endpoint — stays exempt. Paid users
	// (status != trialing) skip entirely.
	if ent, isEntitled := userFromContext.Entitlements[models.AppIDIndexly]; isEntitled && ent.Status == models.SubStatusTrialing {
		if resolved.Status == models.ScheduledArticleStatusScheduled || resolved.Status == models.ScheduledArticleStatusScheduling {
			limit := appCtx.Config.Values.SiteIntelligence.Trial.MaxArticles
			used, cErr := models.GetLifetimeArticlesGeneratedForUser(ctx, userFromContext.ID)
			if cErr != nil {
				logger.Error("Failed to count trial article generations", "error", cErr)
				middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
				return
			}
			if limit > 0 && used >= limit {
				middleware.SendJSONError(w, r, apperrors.TrialLimitReached(limit, used))
				return
			}
		}
	}

	// doesn't override them — keeps the wire payload trim for the common
	// case where the user clicks Generate without editing.
	articleType := req.ArticleType
	if articleType == "" {
		articleType = resolved.ArticleType
	}
	proposedTitle := req.ProposedTitle
	if proposedTitle == "" {
		proposedTitle = resolved.Title
	}

	cgeService := appCtx.InternalServices.ContentGenerationService

	err = cgeService.Orchestrate(ctx, userId, contentGenerationEngine.CGEOrchestratePayload{
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

// thumbnailStyle is the wire view of a prompts.ThumbnailStyleDef, deliberately
// excluding the Template (the raw prompt is never exposed to clients).
type thumbnailStyle struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Swatch      []string `json:"swatch"`
}

// thumbnailStylesResponse is the payload for GET /v1/seo-blog/thumbnail-styles.
// Static, derived from prompts.ThumbnailStyleCatalog — adding a style is a
// backend-only change that surfaces here with no frontend work.
type thumbnailStylesResponse struct {
	DefaultID string           `json:"defaultId"`
	Styles    []thumbnailStyle `json:"styles"`
}

// HandleGetThumbnailStyles returns the curated thumbnail-style catalog for the
// settings picker and the dashboard sidebar. No per-user state — the catalog is
// the same for everyone.
func HandleGetThumbnailStyles(w http.ResponseWriter, r *http.Request) {
	styles := make([]thumbnailStyle, 0, len(prompts.ThumbnailStyleCatalog))
	for _, s := range prompts.ThumbnailStyleCatalog {
		styles = append(styles, thumbnailStyle{
			ID:          s.ID,
			Name:        s.Name,
			Description: s.Description,
			Swatch:      s.Swatch,
		})
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, thumbnailStylesResponse{
		DefaultID: prompts.DefaultThumbnailStyleID,
		Styles:    styles,
	})
}

// retryRequest is the body for POST /v1/seo-blog/retry. The retry path
// intentionally takes only the slot id — title / article type overrides on
// the original orchestrate call are not re-applied because retry's contract
// is "run again with whatever the slot currently holds".
type retryRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
}

// Lives on its own verb so the API surface is explicit about the intent
// (separately throttleable / loggable later if needed).
func HandleRetry(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(retryRequest)
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

	resolved, err := appCtx.InternalServices.ScheduledArticleService.ResolveOrchestrateContext(ctx, userId, req.ScheduledArticleID)
	if err != nil {
		if errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
			middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
			return
		}
		logger.Error("Failed to resolve orchestrate context for retry", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrContentGenerationFailed)
		return
	}

	cgeService := appCtx.InternalServices.ContentGenerationService

	err = cgeService.Orchestrate(ctx, userId, contentGenerationEngine.CGEOrchestratePayload{
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
