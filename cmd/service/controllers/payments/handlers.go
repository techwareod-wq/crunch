package payments

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	paymentsvc "github.com/atharva-ng/crunch/internal/services/paymentService/service"
)

func HandleGetPaymentStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.GetStatus(r.Context(), user)
	if err != nil {
		middleware.GetLogger(r).Error("failed to get payment status", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		return
	}
	// Serve the lean client shape — priceId/tier/features are admin-only and
	// live on the full response returned by the admin status endpoint.
	middleware.SendJSONResponse(w, r, http.StatusOK, resp.ForUser())
}

func HandleGetPlans(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.GetPlans(r.Context(), user.ID)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list plans", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

func HandleCreateCheckoutSession(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.CreateCheckoutSession(r.Context(), user.ID, user.Email)
	if err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrSubscriptionAlreadyActive):
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionConflict)
		case errors.Is(err, paymentsvc.ErrEmailRequired):
			middleware.SendJSONError(w, r, apperrors.ErrUserEmailRequired)
		case errors.Is(err, paymentsvc.ErrPaymentProvider):
			middleware.GetLogger(r).Error("checkout session: paddle call failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		default:
			middleware.GetLogger(r).Error("failed to create checkout session", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type startTrialRequest struct {
	PriceID string `json:"priceId"`
}

// HandleStartTrial provisions a card-less local trial for the plan owning
// priceId. No Paddle checkout, no payment method — the trial lapses to a hard
// paywall after trial_days.
func HandleStartTrial(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	req, ok := r.Context().Value(middleware.DeserializerContextKey).(startTrialRequest)
	if !ok || req.PriceID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	resp, err := appCtx.InternalServices.PaymentService.StartTrial(r.Context(), user.ID, req.PriceID)
	if err != nil {
		switch {
		case errors.Is(err, paymentsvc.ErrTrialNotAvailable):
			middleware.SendJSONError(w, r, apperrors.ErrTrialNotAvailable)
		case errors.Is(err, paymentsvc.ErrTrialAlreadyUsed):
			middleware.SendJSONError(w, r, apperrors.ErrTrialAlreadyUsed)
		case errors.Is(err, paymentsvc.ErrOnboardingIncomplete):
			middleware.SendJSONError(w, r, apperrors.ErrOnboardingIncomplete)
		default:
			middleware.GetLogger(r).Error("failed to start trial", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		}
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

func HandleCancelSubscription(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.CancelSubscription(r.Context(), user.ID)
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

func HandleResubscribe(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.Resubscribe(r.Context(), user.ID)
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

func HandleListTransactions(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.ListTransactions(r.Context(), user.ID)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list transactions", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
