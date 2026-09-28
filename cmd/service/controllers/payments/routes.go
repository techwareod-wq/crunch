package payments

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the payment routes. All are JWT-only — these must stay
// reachable for users WITHOUT a subscription (status, plans, checkout) and
// for lapsed users managing billing, so none take WithAppSubscription.
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/payments/status", http.HandlerFunc(HandleGetPaymentStatus)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/plans", http.HandlerFunc(HandleGetPlans)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/checkout-session", http.HandlerFunc(HandleCreateCheckoutSession)).
		WithJWTAuthentication().
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/start-trial", http.HandlerFunc(HandleStartTrial)).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[startTrialRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/cancel", http.HandlerFunc(HandleCancelSubscription)).
		WithJWTAuthentication().
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/resubscribe", http.HandlerFunc(HandleResubscribe)).
		WithJWTAuthentication().
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/payments/transactions", http.HandlerFunc(HandleListTransactions)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
