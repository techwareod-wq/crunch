package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

// Unknown feature keys must be rejected before any write: a typo'd revoke
// that silently no-ops would leave an admin believing access was removed.
func TestValidateOverrideKeys(t *testing.T) {
	valid := []adminOverrideEntryDTO{{Key: "article.generate"}, {Key: "cms.publish"}}
	if !validateOverrideKeys(valid) {
		t.Error("known feature keys rejected")
	}
	if !validateOverrideKeys(nil) {
		t.Error("empty set must validate (clearing overrides)")
	}
	typo := []adminOverrideEntryDTO{{Key: "article.generate"}, {Key: "cms.pubish"}}
	if validateOverrideKeys(typo) {
		t.Error("unknown feature key must be rejected")
	}
}

// The no-target sweep must default to a dry run like every other migration
// surface — a bare {} that silently rewrote every projection would be the
// one migration without a read-first step.
func TestHandleAdminRecompute_SweepDryRunGate(t *testing.T) {
	orig := backfillEntitlementsSweep
	defer func() { backfillEntitlementsSweep = orig }()

	var gotDryRun bool
	backfillEntitlementsSweep = func(ctx context.Context, dryRun bool) (*models.EntitlementsBackfillReport, error) {
		gotDryRun = dryRun
		return &models.EntitlementsBackfillReport{UsersScanned: 7, Drifted: 2, DriftedUserIDs: []string{"u1", "u2"}}, nil
	}

	// Empty request → dry run, drift reported but nothing recomputed.
	w := runMigrationHandler(t, HandleAdminRecompute, adminRecomputeRequest{})
	if w.Code != http.StatusOK || !gotDryRun {
		t.Fatalf("bare sweep: code=%d dryRun=%v, want 200 + dry run", w.Code, gotDryRun)
	}
	if data := decodeData(t, w); data["dryRun"] != true || data["drifted"] != float64(2) {
		t.Fatalf("dry-run sweep response = %v", data)
	}

	// apply:true → the write pass.
	w = runMigrationHandler(t, HandleAdminRecompute, adminRecomputeRequest{Apply: true})
	if w.Code != http.StatusOK || gotDryRun {
		t.Fatalf("apply sweep: code=%d dryRun=%v, want 200 + apply", w.Code, gotDryRun)
	}
	if data := decodeData(t, w); data["dryRun"] != false {
		t.Fatalf("apply sweep response = %v", data)
	}
}
