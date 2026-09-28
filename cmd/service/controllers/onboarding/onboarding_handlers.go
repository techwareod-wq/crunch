package onboarding

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
)

func HandleUserOnboarding(w http.ResponseWriter, r *http.Request) {
	response := dto.OnboardResponse{}
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dto.OnboardRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userFromRequest := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()

	logger := middleware.GetLogger(r)

	userOnboardingSvc := appCtx.InternalServices.OnboardingService

	webEntityId, _, err := userOnboardingSvc.OnboardUser(ctx, req, userFromRequest)
	if err != nil {
		var appErr *apperrors.Error
		if errors.As(err, &appErr) {
			middleware.SendJSONError(w, r, appErr)
			return
		}
		logger.Error("Failed to onboard user", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityCreationFailed)
		return
	}
	if webEntityId == "" {
		logger.Error("Onboarding service returned empty web entity ID")
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityCreationFailed)
		return
	}
	response.WebEntityID = webEntityId

	// The analysis chain (business context → competitors) runs async on the
	// queue — OnboardUser dispatched it (or re-dispatched a failed run) before
	// returning. The frontend polls /v1/onboarding-steps for progress, so the
	// response only needs the entity ID.
	middleware.SendJSONResponse(w, r, http.StatusOK, response)
}

func HandlePatchWebEntity(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dto.PatchWebEntityRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	applied, finalised, err := appCtx.InternalServices.OnboardingService.PatchOnboardedUser(r.Context(), req, userId)
	if err != nil {
		var appErr *apperrors.Error
		if errors.As(err, &appErr) {
			middleware.SendJSONError(w, r, appErr)
			return
		}
		logger.Error("Failed to patch web entity", "error", err, "webEntityId", req.WebEntityID)
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityPatchFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, dto.PatchWebEntityResponse{
		WebEntityID: req.WebEntityID,
		Applied:     applied,
		Finalised:   finalised,
	})
}

func HandleGetOnboardingSteps(w http.ResponseWriter, r *http.Request) {
	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.OnboardingService.GetOnboardingSteps(r.Context(), userId)
	if err != nil {
		logger.Error("Failed to get onboarding steps", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityPatchFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

func HandleGetCurrentWebEntity(w http.ResponseWriter, r *http.Request) {
	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	entity, err := appCtx.InternalServices.OnboardingService.GetCurrentWebEntity(r.Context(), userId)
	if err != nil {
		var appErr *apperrors.Error
		if errors.As(err, &appErr) {
			middleware.SendJSONError(w, r, appErr)
			return
		}
		logger.Error("Failed to get current web entity", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, entity)
}

func HandleGetPublishingOptions(w http.ResponseWriter, r *http.Request) {
	appCtx := config.GetAppContext(r)
	resp := appCtx.InternalServices.OnboardingService.GetPublishingOptions()
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
