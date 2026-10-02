package search

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Handle registers the public search routes (spec 04): no auth and no rate
// limit (D-018); search takes an optional token so staff searches can be
// told apart in analytics (P-5).
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/public/search", http.HandlerFunc(HandleSearch)).
		WithOptionalJWT().
		With(middleware.DeserializeJson[domain.SearchFilters]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/search/map", http.HandlerFunc(HandleMap)).
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/catalog", http.HandlerFunc(HandleCatalog)).
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/resolve", http.HandlerFunc(HandleResolve)).
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/geo/whereami", http.HandlerFunc(HandleWhereAmI)).
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
