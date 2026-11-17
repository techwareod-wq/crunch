package search

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/ratelimit"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Handle registers the public search routes (spec 04): no rate limit
// (D-018); the /v1/public routes are open to anonymous visitors
// (WithOptionalJWT: a signed-in caller is still identified for analytics),
// behind the shared per-IP publicLimit (internal/ratelimit).
// The /v1/admin/search routes are staff-only.
func Handle(appCtx *config.AppContext, publicLimit *ratelimit.Limiter) {
	middleware.Handle("/v1/public/search", http.HandlerFunc(HandleSearch)).
		WithOptionalJWT().
		With(middleware.DeserializeJson[domain.SearchFilters]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		With(publicLimit.Middleware).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/search/map", http.HandlerFunc(HandleMap)).
		WithOptionalJWT().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		With(publicLimit.Middleware).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/catalog", http.HandlerFunc(HandleCatalog)).
		WithOptionalJWT().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		With(publicLimit.Middleware).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/search", http.HandlerFunc(HandleAdminSearch)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[domain.SearchFilters]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/search/map", http.HandlerFunc(HandleAdminMap)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/search/catalog", http.HandlerFunc(HandleAdminCatalog)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/resolve", http.HandlerFunc(HandleResolve)).
		WithOptionalJWT().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		With(publicLimit.Middleware).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/whereami", http.HandlerFunc(HandleWhereAmI)).
		WithOptionalJWT().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		With(publicLimit.Middleware).
		AllowCORS().
		WithLogEnabled()
}
