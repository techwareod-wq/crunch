package admin

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// setUserAccess is a seam for tests.
var setUserAccess = models.SetUserAccess

type adminSetUserAccessRequest struct {
	TargetUserID string `json:"targetUserId"`
	// Role is "user" or "admin". Superusers are minted only by cmd/superuser.
	Role string `json:"role"`
	// Permissions: any of "editor", "approver". Only meaningful for admin;
	// must be empty for role user.
	Permissions []string `json:"permissions"`
	// ExpectedRoleUpdatedAt is the target's role_updated_at as read (null if
	// never edited) — a concurrent edit gets 409.
	ExpectedRoleUpdatedAt *time.Time `json:"expectedRoleUpdatedAt"`
}

// HandleAdminSetUserAccess serves POST /v1/admin/users/access (superuser): set
// a user's role (user | admin) and an admin's editor/approver permissions.
// Staff onboarding is: the person signs in once, then a superuser sets their
// access here. Superuser accounts are never changed through the API, so a
// superuser can't be demoted or locked out from the panel.
func HandleAdminSetUserAccess(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSetUserAccessRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	perms, valid := normalizeAccess(req.Role, req.Permissions)
	if !valid {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidAccess)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	if target.Role == models.RoleSuperuser {
		middleware.SendJSONError(w, r, apperrors.ErrSuperuserImmutable)
		return
	}

	if err := setUserAccess(r.Context(), target.ID, req.Role, perms, req.ExpectedRoleUpdatedAt); err != nil {
		if errors.Is(err, models.ErrRoleConflictOnUser) {
			middleware.SendJSONError(w, r, apperrors.ErrAccessConflict)
			return
		}
		middleware.GetLogger(r).Error("failed to set user access", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("admin set user access", "role", req.Role, "permissions", perms)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"role": req.Role, "permissions": perms})
}

// normalizeAccess validates role + permissions and returns the permissions
// deduped in canonical order.
func normalizeAccess(role string, perms []string) ([]string, bool) {
	switch role {
	case models.RoleUser:
		return nil, len(perms) == 0
	case models.RoleAdmin:
	default:
		return nil, false
	}
	for _, p := range perms {
		if !authz.IsAssignable(p) {
			return nil, false
		}
	}
	var out []string
	for _, p := range authz.Assignable {
		if slices.Contains(perms, string(p)) {
			out = append(out, string(p))
		}
	}
	return out, true
}
