package company

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	paymentsvc "github.com/atharva-ng/crunch/internal/services/paymentService/service"
)

// Company (team seat) billing endpoints. All ride WithActiveCompany +
// PermCompanyBillingManage; checkout/cancel/resume are additionally
// owner-verified in-service (D12 — the permission gate alone is not enough:
// billing money moves are the owner's, full stop). With no Paddle team
// product configured the whole surface stays dark but well-behaved: checkout
// 400s, the summary returns zeros, cancel/resume 404.

// sendCompanyBillingError maps the payment service's sentinels onto the HTTP
// error catalog — one place, all four handlers.
func sendCompanyBillingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, paymentsvc.ErrTeamPlanNotAvailable):
		middleware.SendJSONError(w, r, apperrors.ErrTeamPlanNotAvailable)
	case errors.Is(err, paymentsvc.ErrCompanyNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrCompanyNotFound)
	case errors.Is(err, paymentsvc.ErrNotCompanyOwner):
		middleware.SendJSONError(w, r, apperrors.ErrNotCompanyOwner)
	case errors.Is(err, paymentsvc.ErrCompanyNotTeam):
		middleware.SendJSONError(w, r, apperrors.ErrPersonalCompanyLocked)
	case errors.Is(err, paymentsvc.ErrCompanyWebsiteRequired):
		middleware.SendJSONError(w, r, apperrors.ErrCompanyWebsiteRequired)
	case errors.Is(err, paymentsvc.ErrSubscriptionAlreadyActive):
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionConflict)
	case errors.Is(err, paymentsvc.ErrNoActiveSubscription):
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionNotFound)
	case errors.Is(err, paymentsvc.ErrEmailRequired):
		middleware.SendJSONError(w, r, apperrors.ErrUserEmailRequired)
	case errors.Is(err, paymentsvc.ErrPaymentProvider):
		middleware.GetLogger(r).Error("company billing: paddle call failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
	default:
		middleware.GetLogger(r).Error("company billing request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
	}
}

// HandleGetCompanyBilling returns the seat/billing summary for the active
// company. Never calls Paddle; an unbilled company answers 200 with zeros.
func HandleGetCompanyBilling(w http.ResponseWriter, r *http.Request) {
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyBillingManage) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	active := middleware.GetActiveCompanyFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.GetCompanyBilling(r.Context(), active.Company.ID)
	if err != nil {
		sendCompanyBillingError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleCompanyCheckout opens a per-seat team checkout for the active company
// (owner-only, enforced in-service). The FE opens the Paddle overlay with
// quantity = seats and passes customData verbatim — that customData.companyId
// is how the webhook links the subscription back to the company.
func HandleCompanyCheckout(w http.ResponseWriter, r *http.Request) {
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyBillingManage) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	active := middleware.GetActiveCompanyFromContext(r)
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.CreateCompanyCheckoutSession(r.Context(), user.ID, user.Email, active.Company.ID)
	if err != nil {
		sendCompanyBillingError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleCompanyCancel schedules cancellation of the company subscription at
// period end (owner-only, in-service). Seats close at the terminal webhook;
// members are never auto-archived.
func HandleCompanyCancel(w http.ResponseWriter, r *http.Request) {
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyBillingManage) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	active := middleware.GetActiveCompanyFromContext(r)
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.CancelCompanySubscription(r.Context(), user.ID, active.Company.ID)
	if err != nil {
		sendCompanyBillingError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleCompanyResume un-cancels a canceling company subscription
// (owner-only, in-service); a fully lapsed one answers checkout_required.
func HandleCompanyResume(w http.ResponseWriter, r *http.Request) {
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyBillingManage) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	active := middleware.GetActiveCompanyFromContext(r)
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)

	resp, err := appCtx.InternalServices.PaymentService.ResumeCompanySubscription(r.Context(), user.ID, active.Company.ID)
	if err != nil {
		sendCompanyBillingError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
