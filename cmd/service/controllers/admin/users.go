package admin

import (
	"net/http"
	"strconv"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// HandleAdminGetProfile mirrors GET /v1/user/profile for a target user
// (?userId=).
func HandleAdminGetProfile(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)

	user, err := appCtx.InternalServices.UserService.GetProfile(r.Context(), target.ID.Hex())
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrUserNotFound)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, user)
}

// HandleAdminLookupUser resolves a support ticket's email (?email=) to a user
// doc — the entry point for finding a target userId.
func HandleAdminLookupUser(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	found, user, err := models.FindUserByEmail(r.Context(), email)
	if err != nil {
		middleware.GetLogger(r).Error("admin user lookup failed", "error", err, "email", email)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrUserNotFound)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, user)
}

// listUsersResponse is paginated from day one — the shape the dashboard's
// tables need.
type listUsersResponse struct {
	Users []models.User `json:"users"`
	Page  int           `json:"page"`
	Limit int           `json:"limit"`
	Total int64         `json:"total"`
}

// HandleAdminListUsers serves the dashboard's landing table: a paginated user
// list (?page=&limit=, 1-based page) with optional case-insensitive email/name
// search (?q=).
func HandleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	pagination := config.GetAppContext(r).Config.Values.Admin.Pagination

	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		page = parsed
	}

	limit := pagination.UsersDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > pagination.UsersMax {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		limit = parsed
	}

	users, total, err := models.ListUsers(r.Context(), page, limit, r.URL.Query().Get("q"))
	if err != nil {
		middleware.GetLogger(r).Error("admin user list failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, listUsersResponse{
		Users: users,
		Page:  page,
		Limit: limit,
		Total: total,
	})
}
