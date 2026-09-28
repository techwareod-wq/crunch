package users

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	errors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
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
