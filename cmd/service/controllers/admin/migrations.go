package admin

import (
	"context"
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

// Test seams.
var (
	seedRolesCatalog       = models.SeedRoles
	migrateCompanyTenancy  = models.MigrateCompanyTenancy
	verifyCompanyTenancy   = models.VerifyCompanyTenancy
	dropWebEntityUserIndex = models.DropWebEntityUserIndex
	ensureTenancyIndexes   = func(ctx context.Context) error {
		// Apply-mode mirror of cmd/companymigrate: the D20 index must exist
		// before the mint pass so idempotence is enforced, not hoped for.
		if err := models.EnsureCompanyIndexes(ctx); err != nil {
			return err
		}
		if err := models.EnsureCompanyMembershipIndexes(ctx); err != nil {
			return err
		}
		return models.EnsureWebEntityIndexes(ctx)
	}
)

// --- roles seed: POST /v1/admin/roles/seed (roles.write) ---

type adminSeedRolesRequest struct {
	Apply bool `json:"apply"`
}

// HandleAdminSeedRoles is the API twin of `rolesmigrate -seed-roles`:
// idempotently upserts the full code-defined catalog (3 system + 2 company
// roles). This is the ONE creation path for company roles — the roles upsert
// endpoint deliberately refuses to mint them (D17: pinned shape).
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

// --- company migrate: POST /v1/admin/company/migrate (migrations.run) ---

// Actions of the company-tenancy migration endpoint (the CLI's flag set).
const (
	migrateActionMigrate       = "migrate"
	migrateActionVerify        = "verify"
	migrateActionDropUserIndex = "drop-user-index"
)

type adminCompanyMigrateRequest struct {
	// Action: "migrate" (default) | "verify" | "drop-user-index".
	Action string `json:"action"`
	Apply  bool   `json:"apply"`
}

// HandleAdminCompanyMigrate is the API twin of cmd/companymigrate. The
// anomaly lists ride the response verbatim — they are load-bearing (each one
// is a doc the backfill could NOT fix), never just log noise.
func HandleAdminCompanyMigrate(w http.ResponseWriter, r *http.Request) {
	req, _ := r.Context().Value(middleware.DeserializerContextKey).(adminCompanyMigrateRequest)
	dryRun := !req.Apply

	switch req.Action {
	case migrateActionVerify:
		report, err := verifyCompanyTenancy(r.Context())
		if err != nil {
			middleware.GetLogger(r).Error("company tenancy verify failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
			"action":                    migrateActionVerify,
			"webEntitiesMissingCompany": report.WebEntitiesMissingCompany,
			"usersWithoutPersonal":      report.UsersWithoutPersonal,
			"legacyUserIndexPresent":    report.LegacyUserIndexPresent,
			"allInvariantsHold":         report.WebEntitiesMissingCompany == 0 && len(report.UsersWithoutPersonal) == 0,
		})

	case migrateActionDropUserIndex:
		pending, err := dropWebEntityUserIndex(r.Context(), dryRun)
		if err != nil {
			// Includes the built-in refusal while unstamped webEntities exist —
			// surface the reason instead of a generic 500 body.
			middleware.GetLogger(r).Error("drop user index refused/failed", "error", err, "dry_run", dryRun)
			middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: err.Error()})
			return
		}
		middleware.GetLogger(r).Info("drop user index ran", "dry_run", dryRun, "index_present", pending)
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
			"action":  migrateActionDropUserIndex,
			"dryRun":  dryRun,
			"present": pending,
			"dropped": pending && !dryRun,
		})

	case "", migrateActionMigrate:
		if !dryRun {
			if err := ensureTenancyIndexes(r.Context()); err != nil {
				middleware.GetLogger(r).Error("tenancy index ensure failed", "error", err)
				middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
				return
			}
		}
		report, err := migrateCompanyTenancy(r.Context(), dryRun)
		if err != nil {
			middleware.GetLogger(r).Error("company tenancy migrate failed", "error", err, "dry_run", dryRun)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		middleware.GetLogger(r).Info("company tenancy migrate ran", "dry_run", dryRun,
			"users", report.Users, "personal_created", report.PersonalCreated,
			"web_entities_stamped", report.WebEntitiesStamped,
			"anomalies", len(report.SkippedNoEmail)+len(report.WebEntitiesUnresolvable)+len(report.WebEntitiesMissingUserID))
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
			"action":                   migrateActionMigrate,
			"dryRun":                   dryRun,
			"users":                    report.Users,
			"personalCreated":          report.PersonalCreated,
			"personalExisting":         report.PersonalExisting,
			"webEntities":              report.WebEntities,
			"webEntitiesStamped":       report.WebEntitiesStamped,
			"webEntitiesAlready":       report.WebEntitiesAlready,
			"skippedNoEmail":           report.SkippedNoEmail,
			"webEntitiesUnresolvable":  report.WebEntitiesUnresolvable,
			"webEntitiesMissingUserID": report.WebEntitiesMissingUserID,
		})

	default:
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
	}
}
