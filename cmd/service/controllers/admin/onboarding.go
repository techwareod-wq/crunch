package admin

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
)

// adminOnboardRequest embeds the user-facing payload and adds the target.
type adminOnboardRequest struct {
	UserID string `json:"userId"`
	dto.OnboardRequest
}

// HandleAdminOnboard mirrors POST /v1/onboard for a target user — white-glove
// onboarding on their behalf. It creates the target's one-and-only WebEntity
// (unique index on user_id); re-onboarding an existing entity is governed by
// OnboardUser's existing semantics, not new admin logic.
func HandleAdminOnboard(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminOnboardRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	webEntityId, _, err := appCtx.InternalServices.OnboardingService.OnboardUser(ctx, req.OnboardRequest, target.ID.Hex())
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

	middleware.SendJSONResponse(w, r, http.StatusOK, dto.OnboardResponse{WebEntityID: webEntityId})
}

// handleAdminWebEntity dispatches /v1/admin/web-entity by method: GET mirrors
// /v1/web-entity/me, PATCH mirrors /v1/web-entity. The route registry is
// path-only, and this pair can't chain DeserializeJson (GET has no body), so
// the PATCH handler decodes its own body.
func handleAdminWebEntity(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		HandleAdminPatchWebEntity(w, r)
		return
	}
	HandleAdminGetWebEntity(w, r)
}

// adminPatchWebEntityRequest embeds the user-facing payload and adds the target.
type adminPatchWebEntityRequest struct {
	UserID string `json:"userId"`
	dto.PatchWebEntityRequest
}

// HandleAdminPatchWebEntity mirrors PATCH /v1/web-entity for a target user.
// The service still enforces the finalised/competitor-lock rules.
func HandleAdminPatchWebEntity(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[adminPatchWebEntityRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	middleware.SanitizeStruct(&req)

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	applied, finalised, err := appCtx.InternalServices.OnboardingService.PatchOnboardedUser(r.Context(), req.PatchWebEntityRequest, target.ID.Hex())
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

// HandleAdminGetWebEntity mirrors GET /v1/web-entity/me for a target user
// (?userId=).
func HandleAdminGetWebEntity(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	entity, err := appCtx.InternalServices.OnboardingService.GetCurrentWebEntity(r.Context(), target.ID.Hex())
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

// HandleAdminGetOnboardingSteps mirrors GET /v1/onboarding-steps for a target
// user (?userId=).
func HandleAdminGetOnboardingSteps(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.OnboardingService.GetOnboardingSteps(r.Context(), target.ID.Hex())
	if err != nil {
		logger.Error("Failed to get onboarding steps", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityPatchFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}
