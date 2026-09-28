package admin

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
)

type adminRerunScheduleRequest struct {
	UserID             string `json:"userId"`
	WebEntityContextID string `json:"webEntityContextId"`
}

// HandleAdminRerunSchedule enqueues SE_RERUN_SCHEDULE for a target user's
// context — appends the next full-mode scheduling window (articles_per_week ×
// scheduling.weeks) after the calendar end. The manual ops trigger for the
// renewal rerun until the webhook trigger is wired; there is no user-facing
// mirror.
func HandleAdminRerunSchedule(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminRerunScheduleRequest)
	if !ok || req.WebEntityContextID == "" {
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

	err := appCtx.InternalServices.SchedulingService.DispatchRerunSchedule(r.Context(), target.ID.Hex(), req.WebEntityContextID)
	if err != nil {
		switch {
		case errors.Is(err, se.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, se.ErrSchedulingNotComplete):
			middleware.SendJSONError(w, r, apperrors.ErrSchedulingNotComplete)
		default:
			logger.Error("Failed to dispatch scheduling rerun", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSchedulingDispatchFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}
