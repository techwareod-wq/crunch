package onboarding

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/onboard", http.HandlerFunc(HandleUserOnboarding)).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dto.OnboardRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/web-entity", http.HandlerFunc(HandlePatchWebEntity)).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dto.PatchWebEntityRequest]()).
		WithMethods("PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/web-entity/me", http.HandlerFunc(HandleGetCurrentWebEntity)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/onboarding-steps", http.HandlerFunc(HandleGetOnboardingSteps)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/publishing-options", http.HandlerFunc(HandleGetPublishingOptions)).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
