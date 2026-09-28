package users

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/user/profile", http.HandlerFunc(HandleGetProfile)).
		With(appCtx.Middleware()).
		WithJWTAuthentication().
		AllowCORS().
		WithMethods("GET").
		WithLogEnabled()
}
