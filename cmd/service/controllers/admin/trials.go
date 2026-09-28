package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	paymentsvc "github.com/atharva-ng/crunch/internal/services/paymentService/service"
)

// Trial ops surface (plan §8): list live trials (with re-trial flags), extend
// a trial, or end one early. Extend/end follow the cancel/resubscribe idiom —
// Paddle call + optimistic local write + recompute; the webhook is the
// authority.

type adminTrialRow struct {
	UserID               string     `json:"userId"`
	UserEmail            string     `json:"userEmail"`
	AppID                string     `json:"appId"`
	PaddleSubscriptionID string     `json:"paddleSubscriptionId"`
	PriceID              string     `json:"priceId"`
	TrialEndsAt          time.Time  `json:"trialEndsAt"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	TrialFlagged         bool       `json:"trialFlagged"`
	// Source is "trial" for card-less local trials, "paddle"/"" otherwise.
	// A local trial can't be converted (no card) — the UI hides that action.
	Source string `json:"source,omitempty"`
}

type adminTrialsResponse struct {
	Trials []adminTrialRow `json:"trials"`
	Total  int64           `json:"total"`
	Page   int             `json:"page"`
}

// HandleAdminListTrials serves GET /v1/admin/trials?appId=&page=&flagged=.
func HandleAdminListTrials(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	flagged := q.Get("flagged") == "true"

	rows, total, err := models.ListTrialingSubscriptions(r.Context(), q.Get("appId"), flagged, page, 50)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list trials", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		return
	}

	resp := adminTrialsResponse{Trials: make([]adminTrialRow, 0, len(rows)), Total: total, Page: page}
	for _, row := range rows {
		appID := row.AppID
		if appID == "" {
			appID = models.AppIDIndexly
		}
		out := adminTrialRow{
			UserID:               row.UserID.Hex(),
			UserEmail:            row.UserEmail,
			AppID:                appID,
			PaddleSubscriptionID: row.PaddleSubscriptionID,
			PriceID:              row.PriceID,
			TrialEndsAt:          row.ValidTill,
			TrialFlagged:         row.TrialFlagged,
			Source:               row.Source,
		}
		if !row.StartedAt.IsZero() {
			started := row.StartedAt
			out.StartedAt = &started
		}
		resp.Trials = append(resp.Trials, out)
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type adminExtendTrialRequest struct {
	TargetUserID string `json:"targetUserId"`
	AppID        string `json:"appId"`
	ExtendDays   int    `json:"extendDays"`
}

// HandleAdminExtendTrial serves POST /v1/admin/trials/extend — Paddle
// next_billed_at update; the webhook confirms.
func HandleAdminExtendTrial(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminExtendTrialRequest)
	if !ok || req.ExtendDays < 1 || req.ExtendDays > 365 {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	newEnd, err := appCtx.InternalServices.PaymentService.ExtendTrial(r.Context(), target.ID, req.AppID, req.ExtendDays)
	if err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrNoTrialingSubscription):
			middleware.SendJSONError(w, r, apperrors.ErrTrialNotFound)
		case errors.Is(err, paymentsvc.ErrPaymentProvider):
			middleware.GetLogger(r).Error("extend trial: paddle call failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		default:
			middleware.GetLogger(r).Error("failed to extend trial", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}

	middleware.GetLogger(r).Info("admin extended trial", "extend_days", req.ExtendDays, "new_trial_end", newEnd)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"trialEndsAt": newEnd})
}

type adminEndTrialRequest struct {
	TargetUserID string `json:"targetUserId"`
	AppID        string `json:"appId"`
	Mode         string `json:"mode"` // convert | cancel
}

// HandleAdminEndTrial serves POST /v1/admin/trials/end.
func HandleAdminEndTrial(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminEndTrialRequest)
	if !ok || (req.Mode != paymentsvc.TrialEndModeConvert && req.Mode != paymentsvc.TrialEndModeCancel) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	if err := appCtx.InternalServices.PaymentService.EndTrial(r.Context(), target.ID, req.AppID, req.Mode); err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrNoTrialingSubscription):
			middleware.SendJSONError(w, r, apperrors.ErrTrialNotFound)
		case errors.Is(err, paymentsvc.ErrTrialNotConvertible):
			middleware.SendJSONError(w, r, apperrors.ErrTrialNotConvertible)
		case errors.Is(err, paymentsvc.ErrPaymentProvider):
			middleware.GetLogger(r).Error("end trial: paddle call failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		default:
			middleware.GetLogger(r).Error("failed to end trial", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}

	middleware.GetLogger(r).Info("admin ended trial", "mode", req.Mode)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"mode": req.Mode})
}
