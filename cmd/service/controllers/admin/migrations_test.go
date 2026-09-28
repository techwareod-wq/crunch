package admin

import (
	"context"
	"encoding/json"
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
		if len(incoming) < 3 {
			t.Errorf("seed catalog has %d roles, want the full 3 system roles", len(incoming))
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
