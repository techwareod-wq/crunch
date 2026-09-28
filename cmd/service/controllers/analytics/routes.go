// Package analytics is the /v1/analytics controller: generic metric endpoints
// (series/kpis), the three bespoke tables, and the per-source connection
// lifecycle. The whole surface rides gsc.enabled (404 masquerade via
// WithAnalyticsFeature) + the analytics.view feature (granted in every plan —
// declared-but-universal) + the active-company entity resolution.
package analytics

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	// Connection lifecycle (generic per source — the source name travels in
	// the body, matching the house flat-path convention).

	middleware.Handle("/v1/analytics/connections", http.HandlerFunc(HandleGetConnections)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/connections/verify", http.HandlerFunc(HandleVerifyConnection)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[connectionRequest]()).
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/connections/disconnect", http.HandlerFunc(HandleDisconnectConnection)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[connectionRequest]()).
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	// Generic metric endpoints (metric registry).

	middleware.Handle("/v1/analytics/series", http.HandlerFunc(HandleSeries)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/kpis", http.HandlerFunc(HandleKPIs)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	// Bespoke tables.

	middleware.Handle("/v1/analytics/pages", http.HandlerFunc(HandlePagesTable)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/articles", http.HandlerFunc(HandleArticlesTable)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/article", http.HandlerFunc(HandleArticleDetail)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/analytics/queries", http.HandlerFunc(HandleQueriesTable)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureAnalyticsView).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithAnalyticsFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()
}
