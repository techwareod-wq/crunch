package audit

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Handle registers the audit surface: the tenant dashboard routes (JWT +
// entitlement + active company, the styleReplication chain) and the
// Clerk-gated FREE-audit funnel routes (JWT only — no entitlement/company
// gate, a fresh sign-up has neither; the §11 abuse guards cap the cost).
// The whole surface rides audit.enabled (404 masquerade).
//
// The external landing page never calls this API — it only links into the
// app's /free-audit page — so no marketing-origin CORS entry is needed; the
// app origin is already allowed.
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/audit/run", http.HandlerFunc(HandleRun)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAudit).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAuditFeature().
		AllowCORS().
		WithMethods(http.MethodGet, http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/audit/reports", http.HandlerFunc(HandleListReports)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAudit).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAuditFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/audit/recheck", http.HandlerFunc(HandleRecheck)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAudit).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAuditFeature().
		AllowCORS().
		WithMethods(http.MethodGet, http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/audit/free", http.HandlerFunc(HandleFreeStart)).
		With(middleware.DeserializeJson[freeStartRequest]()).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAuditFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/audit/free/report", http.HandlerFunc(HandleFreeReport)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAuditFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()
}
