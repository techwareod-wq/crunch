package users

import (
	stderrors "errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	errors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
)

func HandleGetProfile(w http.ResponseWriter, r *http.Request) {
	appCtx := config.GetAppContext(r)

	// User is injected by WithJWTAuthentication middleware — panics if middleware was skipped (by design)
	userFromToken := middleware.GetUserFromContext(r)

	user, err := appCtx.InternalServices.UserService.GetProfile(r.Context(), userFromToken.ID.Hex())
	if err != nil {
		middleware.SendJSONError(w, r, errors.ErrUserNotFound)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, user)
}

// HandleUpdateProfile serves POST /v1/me/profile: stores the caller's phone
// (validated to E.164, default region IN) and company (D-100).
func HandleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dto.UpdateProfileRequest)
	if !ok {
		middleware.SendJSONError(w, r, errors.ErrInvalidRequestBody)
		return
	}
	appCtx := config.GetAppContext(r)
	caller := middleware.GetUserFromContext(r)

	user, err := appCtx.InternalServices.UserService.UpdateProfile(r.Context(), caller.ID.Hex(), req)
	if err != nil {
		var appErr *errors.Error
		if stderrors.As(err, &appErr) {
			middleware.SendJSONError(w, r, appErr)
			return
		}
		middleware.GetLogger(r).Error("update profile failed", "error", err)
		middleware.SendJSONError(w, r, errors.ErrUserNotFound)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, user)
}
