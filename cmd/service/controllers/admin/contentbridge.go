package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
)

type adminPublishRequest struct {
	UserID             string `json:"userId"`
	ScheduledArticleID string `json:"scheduledArticleId"`
}

// HandleAdminPublishArticle mirrors POST /v1/content-bridge/publish for a
// target user — the most outward-facing action in the system: it publishes to
// the target's live CMS. The dispatch carries the target's userId, so the
// async worker re-verifies ownership exactly as it does for user-initiated
// publishes.
func HandleAdminPublishArticle(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminPublishRequest)
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

	err := appCtx.InternalServices.Dispatcher.Dispatch(r.Context(),
		string(contentBridge.ProcessContentBridgePublish),
		target.ID.Hex(),
		contentBridge.PublishPayload{ScheduledArticleID: req.ScheduledArticleID},
	)
	if err != nil {
		logger.Error("Failed to dispatch content bridge publish", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPublishDispatchFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "publishing"})
}
