package seoblog

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/seo-blog/orchestrate", http.HandlerFunc(HandleOrchestrate)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleGenerate).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[orchestrateRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/seo-blog/retry", http.HandlerFunc(HandleRetry)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleGenerate).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[retryRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Static thumbnail-style catalog for the settings picker + dashboard sidebar.
	// Same auth chain as the sibling generation routes.
	middleware.Handle("/v1/seo-blog/thumbnail-styles", http.HandlerFunc(HandleGetThumbnailStyles)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
