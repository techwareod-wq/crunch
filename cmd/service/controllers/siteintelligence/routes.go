package siteintelligence

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/site-intelligence/process", http.HandlerFunc(HandleProcess)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureSiteProcess).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[processRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/dispatch-funnel-classification", http.HandlerFunc(HandleDispatchFunnelClassification)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordResearch).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dispatchFunnelClassificationRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/dispatch-opportunity-score", http.HandlerFunc(HandleDispatchOpportunityScore)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordResearch).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dispatchOpportunityScoreRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/dispatch-clustering", http.HandlerFunc(HandleDispatchClustering)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordResearch).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dispatchClusteringRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/manual-keyword", http.HandlerFunc(HandleAddManualKeyword)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordManual).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[addManualKeywordRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/keyword-data", http.HandlerFunc(HandleGetKeywordData)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/cluster-supporting", http.HandlerFunc(HandleGetClusterSupporting)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/site-intelligence/keyword-search", http.HandlerFunc(HandleSearchKeywords)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureKeywordView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
