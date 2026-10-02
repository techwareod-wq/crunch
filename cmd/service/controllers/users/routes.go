package users

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/user/profile", http.HandlerFunc(HandleGetProfile)).
		With(appCtx.Middleware()).
		WithJWTAuthentication().
		AllowCORS().
		WithMethods("GET").
		WithLogEnabled()

	// Visitor profile fields (D-100): phone + company, prefilled on enquiries.
	middleware.Handle("/v1/me/profile", http.HandlerFunc(HandleUpdateProfile)).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dto.UpdateProfileRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
