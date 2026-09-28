package contentbridge

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/content-bridge/publish", http.HandlerFunc(HandlePublishArticle)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureCMSPublish).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[publishRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// No subscription gate: collections are listed during onboarding, which
	// happens before the payment step. POST (not GET) because the API key
	// travels in the body.
	middleware.Handle("/v1/content-bridge/collections", http.HandlerFunc(HandleListCollections)).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[listCollectionsRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Schema of the SAVED publishing config's collection — the article
	// surfaces read platform capabilities (Payload draftsEnabled) from it.
	// No subscription gate: it renders warnings, it can't publish anything.
	middleware.Handle("/v1/content-bridge/schema", http.HandlerFunc(HandleGetBlogSchema)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
