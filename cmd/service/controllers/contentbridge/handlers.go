package contentbridge

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms/sidecar"
	cbservice "github.com/atharva-ng/crunch/internal/services/contentBridge/service"
)

type publishRequest struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
}

type listCollectionsRequest struct {
	Platform   string `json:"platform"`
	ApiKey     string `json:"apiKey"`
	ProjectURL string `json:"projectUrl"`
	// AuthCollection is Payload-only: the collection the API key's user lives
	// in. Optional — empty falls back to the adapter default ("users").
	AuthCollection string `json:"authCollection"`
}

type listCollectionsResponse struct {
	Collections []dto.CollectionSummary `json:"collections"`
}

// HandlePublishArticle validates ownership of the request, then DISPATCHES the
// publish onto the async worker pool and returns 202 Accepted. The publish
// itself (schema fetch + CMS write + state persistence) runs in HandlePublish.
func HandlePublishArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(publishRequest)
	if !ok || req.ScheduledArticleID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.Dispatcher.Dispatch(ctx,
		string(contentBridge.ProcessContentBridgePublish),
		userId,
		contentBridge.PublishPayload{ScheduledArticleID: req.ScheduledArticleID},
	)
	if err != nil {
		logger.Error("Failed to dispatch content bridge publish", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPublishDispatchFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "publishing"})
}

// HandleListCollections lists the CMS collections reachable with the
// credentials in the request body. Called from onboarding BEFORE the
// publishing config is saved, so credentials travel with the call — nothing
// is persisted here.
func HandleListCollections(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(listCollectionsRequest)
	if !ok || req.Platform == "" || req.ApiKey == "" || req.ProjectURL == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	collections, err := appCtx.InternalServices.ContentBridgeService.ListCollections(
		r.Context(), req.Platform, req.ApiKey, req.ProjectURL, req.AuthCollection,
	)
	if err != nil {
		switch {
		case errors.Is(err, cbservice.ErrInvalidCredentials):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		case sidecar.IsPermanent(err):
			// The platform understood us and said no — bad token, wrong
			// project URL. Retrying the same credentials won't help.
			middleware.SendJSONError(w, r, apperrors.ErrCollectionListRejected)
		default:
			logger.Error("Failed to list collections", "platform", req.Platform, "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCollectionListFailed)
		}
		return
	}

	if collections == nil {
		collections = []dto.CollectionSummary{}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, listCollectionsResponse{Collections: collections})
}

// HandleGetBlogSchema returns the configured platform's collection schema for
// the SAVED publishing config. The article surfaces read platform
// capabilities from it — notably Payload's draftsEnabled flag, which drives
// the "pushes go live immediately" warning when the user's collection has
// drafts disabled.
func HandleGetBlogSchema(w http.ResponseWriter, r *http.Request) {
	userId := middleware.GetUserFromContext(r).ID.Hex()
	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	schema, err := appCtx.InternalServices.ContentBridgeService.GetBlogStructure(r.Context(), userId)
	if err != nil {
		switch {
		case errors.Is(err, cbservice.ErrWebEntityNotFound),
			errors.Is(err, cbservice.ErrPublishingNotConfigured):
			middleware.SendJSONError(w, r, apperrors.ErrSchemaFetchRejected)
		case sidecar.IsPermanent(err):
			// The platform understood us and said no — revoked key, deleted
			// collection. Retrying the same saved config won't help.
			middleware.SendJSONError(w, r, apperrors.ErrSchemaFetchRejected)
		default:
			logger.Error("Failed to fetch blog schema", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSchemaFetchFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, schema)
}
