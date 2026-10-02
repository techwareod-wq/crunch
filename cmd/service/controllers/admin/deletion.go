package admin

import (
	"context"
	"net/http"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
)

// Seams for guard tests (findUserByID idiom from target.go).
var (
	findUserByIDIncludingDeactivated = models.FindUserByIDIncludingDeactivated

	deleteUserAccountFn = func(r *http.Request, target *models.User, adminEmail string) (*accountService.DeletionReport, error) {
		return config.GetAppContext(r).InternalServices.AccountService.DeleteUserAccount(r.Context(), target, adminEmail)
	}
)

// resolveTargetUserIncludingDeactivated is resolveTargetUser without the
// active filter, for the delete-user endpoint ONLY: mid-cascade the Clerk
// user.deleted webhook may tombstone the target, and a retry of the delete
// must still resolve it to finish the teardown. Every other admin endpoint
// keeps the deliberate 404-on-deactivated behavior.
func resolveTargetUserIncludingDeactivated(r *http.Request, userId string) (*models.User, *http.Request, *apperrors.Error) {
	if userId == "" {
		return nil, r, apperrors.ErrInvalidRequestBody
	}
	if _, err := primitive.ObjectIDFromHex(userId); err != nil {
		return nil, r, apperrors.ErrInvalidRequestBody
	}

	found, target, err := findUserByIDIncludingDeactivated(r.Context(), userId)
	if err != nil {
		middleware.GetLogger(r).Error("admin target user lookup failed", "error", err, "target_user_id", userId)
		return nil, r, apperrors.ErrAdminCheckFailed
	}
	if !found {
		return nil, r, apperrors.ErrUserNotFound
	}

	adminUser := middleware.GetUserFromContext(r)
	logger := middleware.GetLogger(r).With(
		"admin_email", adminUser.Email,
		"target_user_id", target.ID.Hex(),
		"target_email", target.Email,
	)
	ctx := context.WithValue(r.Context(), middleware.LoggerContextKey, logger)
	return target, r.WithContext(ctx), nil
}

// --- account deletion: POST /v1/admin/users/delete (users.delete) ---

type adminDeleteUserRequest struct {
	UserID string `json:"userId"`
}

// HandleAdminDeleteUser destroys the target's account: every registered
// feature's data (accountService.DataCleaner), the Clerk user, and finally the
// user doc soft-deleted with PII scrubbed (adminActions survive).
// Guards: no self-deletion, and no superuser targets (superusers are managed
// only by cmd/superuser).
func HandleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminDeleteUserRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUserIncludingDeactivated(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	adminUser := middleware.GetUserFromContext(r)
	if target.ID == adminUser.ID {
		middleware.SendJSONError(w, r, apperrors.ErrSelfDeletion)
		return
	}
	if target.Role == models.RoleSuperuser {
		middleware.SendJSONError(w, r, apperrors.ErrSuperuserUndeletable)
		return
	}

	report, err := deleteUserAccountFn(r, target, adminUser.Email)
	if err != nil {
		middleware.GetLogger(r).Error("user deletion failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	middleware.GetLogger(r).Info("admin deleted user account", "report", report)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"report": report})
}
