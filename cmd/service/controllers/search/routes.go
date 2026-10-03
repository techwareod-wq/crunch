package search

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Handle registers the public search routes (spec 04): no rate limit
// (D-018); every route needs sign-in plus the search feature (WithFeature).
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/public/search", http.HandlerFunc(HandleSearch)).
		WithFeature(authz.FeatureSearch).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[domain.SearchFilters]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/search/map", http.HandlerFunc(HandleMap)).
		WithFeature(authz.FeatureSearch).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/catalog", http.HandlerFunc(HandleCatalog)).
		WithFeature(authz.FeatureSearch).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/resolve", http.HandlerFunc(HandleResolve)).
		WithFeature(authz.FeatureSearch).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/whereami", http.HandlerFunc(HandleWhereAmI)).
		WithFeature(authz.FeatureSearch).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
