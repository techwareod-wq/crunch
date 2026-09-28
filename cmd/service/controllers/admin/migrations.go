package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// This file is the panel twin of the migration CLIs — the same model-layer
// calls behind superuser-gated routes, so routine cutover steps don't need an
// SSH session. Everything defaults to a dry run; writes require an explicit
// "apply": true. The CLIs remain the recovery path (they work while the
// service is down, and rolesmigrate's lockout hatches are deliberately
// CLI-only — an API that can fix an API lockout is no hatch at all).

// Test seam.
var seedRolesCatalog = models.SeedRoles

// --- roles seed: POST /v1/admin/roles/seed (roles.write) ---

type adminSeedRolesRequest struct {
	Apply bool `json:"apply"`
}

// HandleAdminSeedRoles is the API twin of `rolesmigrate -seed-roles`:
// idempotently upserts the full code-defined catalog (the 3 system roles).
func HandleAdminSeedRoles(w http.ResponseWriter, r *http.Request) {
	req, _ := r.Context().Value(middleware.DeserializerContextKey).(adminSeedRolesRequest)
	appCtx := config.GetAppContext(r)

	dryRun := !req.Apply
	report, err := seedRolesCatalog(r.Context(), authz.DefaultRoles(), dryRun)
	if err != nil {
		middleware.GetLogger(r).Error("roles seed failed", "error", err, "dry_run", dryRun)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !dryRun && (report.Created > 0 || report.Updated > 0) {
		reloadRolesCache(r, appCtx)
	}
	middleware.GetLogger(r).Info("roles seed ran",
		"dry_run", dryRun, "created", report.Created, "updated", report.Updated, "unchanged", report.Unchanged)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"dryRun":    dryRun,
		"created":   report.Created,
		"updated":   report.Updated,
		"unchanged": report.Unchanged,
	})
}
