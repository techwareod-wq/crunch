package users

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
)

func Handle(appCtx *config.AppContext) {
	// Sign-in only (no feature gate): it returns the caller's features, so a
	// user without access can still be told so.
	middleware.Handle("/v1/user/profile", http.HandlerFunc(HandleGetProfile)).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Visitor profile fields (D-100): phone + company, prefilled on enquiries.
	middleware.Handle("/v1/me/profile", http.HandlerFunc(HandleUpdateProfile)).
		WithFeature(authz.FeatureAccess).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dto.UpdateProfileRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
