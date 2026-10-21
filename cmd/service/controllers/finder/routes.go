// Package finder is the EXPERIMENTAL Warehouse Finder: a filter search over
// live warehouses (place + radius, area, type, features, near-hub) and the
// top-3 wizard (/match), both matched by plain Go rules (no AI). It reads
// the `warehouses` collection directly and never writes. To uninstall,
// delete this package and its finder.Handle line in cmd/service/app-service.go.
package finder

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the finder routes. Anonymous callers are allowed
// (WithOptionalJWT): the routes only read live listings.
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/public/finder/cities", http.HandlerFunc(HandleCities)).
		WithOptionalJWT().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/finder/match", http.HandlerFunc(HandleMatch)).
		WithOptionalJWT().
		With(middleware.DeserializeJson[matchRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/finder/search", http.HandlerFunc(HandleSearch)).
		WithOptionalJWT().
		With(middleware.DeserializeJson[searchRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
