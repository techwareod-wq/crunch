package aisearch

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
)

// Handle registers AI search (spec 05): the public natural-language search
// (sign-in plus the ai_search feature) and the superuser embedding
// backfill. Nothing is registered while the feature flag
// (warehousehub.aisearch.enabled) is off.
func Handle(appCtx *config.AppContext) {
	if !appCtx.Config.Values.WarehouseHub.AISearch.Enabled {
		return
	}

	// Per-user rate limit (ratelimit.go): chained first so it runs after
	// sign-in and the feature check.
	middleware.Handle("/v1/public/search/ai", http.HandlerFunc(HandleSearch)).
		With(userLimiterFromEnv().middleware).
		WithFeature(authz.FeatureAISearch).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[aiSearchService.Request]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/aisearch/reembed-all", http.HandlerFunc(HandleReembedAll)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
