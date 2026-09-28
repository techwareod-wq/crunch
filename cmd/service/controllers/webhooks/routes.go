package webhooks

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/webhooks/clerk", http.HandlerFunc(HandleClerkWebhook)).
		With(appCtx.Middleware()).
		AllowCORS().
		WithMethods("POST").
		WithLogEnabled()

	middleware.Handle("/v1/webhooks/paddle", http.HandlerFunc(HandlePaddleWebhook)).
		With(appCtx.Middleware()).
		AllowCORS().
		WithMethods("POST").
		WithLogEnabled()
}
