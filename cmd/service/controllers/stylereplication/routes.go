package stylereplication

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/style-replication/run", http.HandlerFunc(HandleRun)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodGet, http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/style-replication/run/urls", http.HandlerFunc(HandleSubmitURLs)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[submitURLsRequest]()).
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/style-replication/run/upload-url", http.HandlerFunc(HandleCreateUploadURL)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[uploadURLRequest]()).
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/style-replication/run/approve", http.HandlerFunc(HandleApprove)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[approveRequest]()).
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/style-replication/run/cancel", http.HandlerFunc(HandleCancelRun)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/style-replication/profile", http.HandlerFunc(HandleDeleteProfile)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureStyleReplication).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithStyleReplicationFeature().
		AllowCORS().
		WithMethods(http.MethodDelete).
		WithLogEnabled()
}
