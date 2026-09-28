package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// runMigrationHandler drives a handler through the AppContext middleware with
// an optional pre-deserialized body, mirroring the real chain's context.
func runMigrationHandler(t *testing.T, handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/test", nil)
	if body != nil {
		r = r.WithContext(context.WithValue(r.Context(), middleware.DeserializerContextKey, body))
	}
	w := httptest.NewRecorder()
	(&config.AppContext{}).Middleware()(handler).ServeHTTP(w, r)
	return w
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope.Data
}

func TestHandleAdminSeedRoles_DryRunDefaultAndApplyReload(t *testing.T) {
	origSeed, origReload := seedRolesCatalog, reloadRolesCache
	defer func() { seedRolesCatalog, reloadRolesCache = origSeed, origReload }()

	var gotDryRun bool
	seedRolesCatalog = func(ctx context.Context, incoming []models.Role, dryRun bool) (*models.RoleSeedReport, error) {
		gotDryRun = dryRun
		if len(incoming) < 5 {
			t.Errorf("seed catalog has %d roles, want the full 5 (3 system + 2 company)", len(incoming))
		}
		return &models.RoleSeedReport{Created: 2, Unchanged: 3}, nil
	}
	reloaded := false
	reloadRolesCache = func(r *http.Request, appCtx *config.AppContext) { reloaded = true }

	// No body → dry run; the cache must NOT reload on a read-only pass.
	w := runMigrationHandler(t, HandleAdminSeedRoles, nil)
	if w.Code != http.StatusOK || !gotDryRun {
		t.Fatalf("bodyless call: code=%d dryRun=%v, want 200 + dry run", w.Code, gotDryRun)
	}
	if reloaded {
		t.Fatal("dry run reloaded the roles cache")
	}
	if data := decodeData(t, w); data["dryRun"] != true || data["created"] != float64(2) {
		t.Fatalf("dry-run response = %v", data)
	}

	// apply:true → writes + cache reload.
	w = runMigrationHandler(t, HandleAdminSeedRoles, adminSeedRolesRequest{Apply: true})
	if w.Code != http.StatusOK || gotDryRun {
		t.Fatalf("apply call: code=%d dryRun=%v, want 200 + apply", w.Code, gotDryRun)
	}
	if !reloaded {
		t.Fatal("apply with writes did not reload the roles cache")
	}
}

func TestHandleAdminCompanyMigrate_ActionDispatch(t *testing.T) {
	origMigrate, origVerify, origDrop, origEnsure := migrateCompanyTenancy, verifyCompanyTenancy, dropWebEntityUserIndex, ensureTenancyIndexes
	defer func() {
		migrateCompanyTenancy, verifyCompanyTenancy, dropWebEntityUserIndex, ensureTenancyIndexes = origMigrate, origVerify, origDrop, origEnsure
	}()

	var migrateDry *bool
	ensured := false
	migrateCompanyTenancy = func(ctx context.Context, dryRun bool) (*models.CompanyMigrationReport, error) {
		migrateDry = &dryRun
		return &models.CompanyMigrationReport{Users: 4, PersonalCreated: 1, WebEntitiesUnresolvable: []string{"we1"}}, nil
	}
	verifyCompanyTenancy = func(ctx context.Context) (*models.CompanyTenancyVerifyReport, error) {
		return &models.CompanyTenancyVerifyReport{LegacyUserIndexPresent: true}, nil
	}
	dropWebEntityUserIndex = func(ctx context.Context, dryRun bool) (bool, error) {
		return false, errors.New("refusing to drop: 3 webEntity docs still have no company_id")
	}
	ensureTenancyIndexes = func(ctx context.Context) error { ensured = true; return nil }

	// Bodyless default = migrate, dry run, indexes untouched.
	w := runMigrationHandler(t, HandleAdminCompanyMigrate, nil)
	if w.Code != http.StatusOK || migrateDry == nil || !*migrateDry || ensured {
		t.Fatalf("default action: code=%d dry=%v ensured=%v", w.Code, migrateDry, ensured)
	}
	if data := decodeData(t, w); data["action"] != "migrate" || len(data["webEntitiesUnresolvable"].([]any)) != 1 {
		t.Fatalf("migrate response = %v (anomalies must ride the response)", data)
	}

	// Apply ensures indexes first.
	w = runMigrationHandler(t, HandleAdminCompanyMigrate, adminCompanyMigrateRequest{Action: "migrate", Apply: true})
	if w.Code != http.StatusOK || *migrateDry || !ensured {
		t.Fatalf("apply migrate: code=%d dry=%v ensured=%v", w.Code, *migrateDry, ensured)
	}

	// Verify is read-only reporting.
	w = runMigrationHandler(t, HandleAdminCompanyMigrate, adminCompanyMigrateRequest{Action: "verify"})
	if data := decodeData(t, w); w.Code != http.StatusOK || data["allInvariantsHold"] != true || data["legacyUserIndexPresent"] != true {
		t.Fatalf("verify: code=%d data=%v", w.Code, data)
	}

	// The drop refusal surfaces as a 409 with the reason, not a generic 500.
	w = runMigrationHandler(t, HandleAdminCompanyMigrate, adminCompanyMigrateRequest{Action: "drop-user-index"})
	if w.Code != http.StatusConflict {
		t.Fatalf("drop refusal: code=%d, want 409", w.Code)
	}

	// Unknown action → 400.
	w = runMigrationHandler(t, HandleAdminCompanyMigrate, adminCompanyMigrateRequest{Action: "bogus"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bogus action: code=%d, want 400", w.Code)
	}
}
