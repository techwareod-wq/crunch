package analytics

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the search analytics dashboards (spec 07), approver
// only (D-112).
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/admin/analytics/search/overview", http.HandlerFunc(HandleOverview)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/search/top", http.HandlerFunc(HandleTop)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/search/zero-results", http.HandlerFunc(HandleZeroResults)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/search/conversion", http.HandlerFunc(HandleConversion)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/search/log", http.HandlerFunc(HandleLog)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
