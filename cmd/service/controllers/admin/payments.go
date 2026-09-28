package admin

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	paymentsvc "github.com/atharva-ng/crunch/internal/services/paymentService/service"
)

// No checkout-session mirror: checkout is the customer's own browser flow —
// an admin-created Paddle session for someone else's email is a footgun, and
// manual/custom payments belong to the payment-service expansion plan.

// HandleAdminGetPaymentStatus mirrors GET /v1/payments/status for a target
// user (?userId=).
func HandleAdminGetPaymentStatus(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.GetStatus(r.Context(), target)
	if err != nil {
		middleware.GetLogger(r).Error("failed to get payment status", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleAdminGetPlans mirrors GET /v1/payments/plans for a target user
// (?userId=) — shows the target's trial eligibility.
func HandleAdminGetPlans(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.GetPlans(r.Context(), target.ID)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list plans", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleAdminListTransactions mirrors GET /v1/payments/transactions for a
// target user (?userId=) — full billing history.
func HandleAdminListTransactions(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.ListTransactions(r.Context(), target.ID)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list transactions", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type adminCancelSubscriptionRequest struct {
	UserID string `json:"userId"`
}

// HandleAdminCancelSubscription mirrors POST /v1/payments/cancel for a target
// user — cancels a paying customer's subscription, a support-request-only
// action.
func HandleAdminCancelSubscription(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminCancelSubscriptionRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.CancelSubscription(r.Context(), target.ID)
	if err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrNoActiveSubscription):
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionNotFound)
		case errors.Is(err, paymentsvc.ErrPaymentProvider):
			middleware.GetLogger(r).Error("cancel subscription: paddle call failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		default:
			middleware.GetLogger(r).Error("failed to cancel subscription", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type adminResubscribeRequest struct {
	UserID string `json:"userId"`
}

// HandleAdminResubscribe mirrors POST /v1/payments/resubscribe for a target
// user — un-cancels a canceling subscription.
func HandleAdminResubscribe(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminResubscribeRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.Resubscribe(r.Context(), target.ID)
	if err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrSubscriptionAlreadyActive):
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionConflict)
		case errors.Is(err, paymentsvc.ErrPaymentProvider):
			middleware.GetLogger(r).Error("resubscribe: paddle call failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		default:
			middleware.GetLogger(r).Error("failed to resubscribe", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
