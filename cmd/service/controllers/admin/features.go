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

// setUserFeatures is a seam for tests.
var setUserFeatures = models.SetUserFeatures

type adminSetUserFeaturesRequest struct {
	TargetUserID string `json:"targetUserId"`
	// Features: any of authz.Features. Empty removes all access.
	Features []string `json:"features"`
	// ExpectedRoleUpdatedAt is the target's role_updated_at as read (null if
	// never edited) — a concurrent edit gets 409.
	ExpectedRoleUpdatedAt *time.Time `json:"expectedRoleUpdatedAt"`
}

// HandleAdminSetUserFeatures serves POST /v1/admin/users/features
// (superuser): replace the visitor features a user may use (WithFeature).
// Staff hold every feature regardless, so this is only meaningful for role
// user.
func HandleAdminSetUserFeatures(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSetUserFeaturesRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	features, valid := normalizeFeatures(req.Features)
	if !valid {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidFeatures)
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

	if err := setUserFeatures(r.Context(), target.ID, features, req.ExpectedRoleUpdatedAt); err != nil {
		if errors.Is(err, models.ErrRoleConflictOnUser) {
			middleware.SendJSONError(w, r, apperrors.ErrAccessConflict)
			return
		}
		middleware.GetLogger(r).Error("failed to set user features", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("admin set user features", "features", features)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"features": features})
}

// normalizeFeatures validates features and returns them deduped in canonical
// order.
func normalizeFeatures(features []string) ([]string, bool) {
	for _, f := range features {
		if !authz.IsFeature(f) {
			return nil, false
		}
	}
	var out []string
	for _, f := range authz.Features {
		if slices.Contains(features, string(f)) {
			out = append(out, string(f))
		}
	}
	return out, true
}
