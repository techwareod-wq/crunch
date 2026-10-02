package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// This file exercises the privilege-escalation guards (plan §7) at the HANDLER
// level — the pure validators are covered in roles_test.go, but the guards that
// sit between DB reads/writes (rank ceiling, grant-only-what-you-hold,
// superuser containment, immutable roles, last-superuser lockout) are only
// verified end-to-end here. Every DB touch is routed through the package-var
// seams in roles.go, so these run in-memory with no Mongo.

// --- seams ------------------------------------------------------------------

func withFindRoleSeam(t *testing.T, fn func(ctx context.Context, key string) (bool, *models.Role, error)) {
	t.Helper()
	orig := findRoleByKey
	findRoleByKey = fn
	t.Cleanup(func() { findRoleByKey = orig })
}

func withCreateRoleSeam(t *testing.T, fn func(ctx context.Context, r *models.Role) error) {
	t.Helper()
	orig := createRole
	createRole = fn
	t.Cleanup(func() { createRole = orig })
}

func withUpdateRoleSeam(t *testing.T, fn func(ctx context.Context, r *models.Role, expected *time.Time) error) {
	t.Helper()
	orig := updateRole
	updateRole = fn
	t.Cleanup(func() { updateRole = orig })
}

func withDeleteRoleSeam(t *testing.T, fn func(ctx context.Context, key string) error) {
	t.Helper()
	orig := deleteRoleByKey
	deleteRoleByKey = fn
	t.Cleanup(func() { deleteRoleByKey = orig })
}

func withCountUsersWithRoleSeam(t *testing.T, fn func(ctx context.Context, key string) (int64, error)) {
	t.Helper()
	orig := countUsersWithRole
	countUsersWithRole = fn
	t.Cleanup(func() { countUsersWithRole = orig })
}

func withCountSuperusersSeam(t *testing.T, fn func(ctx context.Context) (int64, error)) {
	t.Helper()
	orig := countActiveSuperusers
	countActiveSuperusers = fn
	t.Cleanup(func() { countActiveSuperusers = orig })
}

func withSetUserRoleSeam(t *testing.T, fn func(ctx context.Context, id primitive.ObjectID, role string, expected *time.Time) error) {
	t.Helper()
	orig := setUserRole
	setUserRole = fn
	t.Cleanup(func() { setUserRole = orig })
}

// withReloadNoop stubs the write-through cache reload (it would otherwise hit a
// nil DB on the success paths). The cache content is asserted via the seeded
// AppContext, not the reload.
func withReloadNoop(t *testing.T) {
	t.Helper()
	orig := reloadRolesCache
	reloadRolesCache = func(r *http.Request, appCtx *config.AppContext) {}
	t.Cleanup(func() { reloadRolesCache = orig })
}

// --- request fixtures -------------------------------------------------------

// guardAppCtx builds an AppContext whose RolesCache resolves the given catalog.
func guardAppCtx(t *testing.T, roles []models.Role) *config.AppContext {
	t.Helper()
	cache, err := authz.NewRolesCacheFromRoles(roles)
	if err != nil {
		t.Fatalf("build roles cache: %v", err)
	}
	return &config.AppContext{RolesCache: cache}
}

// callerUser is a caller whose Role resolves against the test cache.
func callerUser(role string) *models.User {
	return &models.User{ID: primitive.NewObjectID(), Email: "caller@x.com", Role: role}
}

// serveGuard runs handler with appCtx injected (via the appctx middleware, as
// production does) over a request carrying caller + optional deserialized body.
func serveGuard(appCtx *config.AppContext, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	appCtx.Middleware()(handler).ServeHTTP(rec, r)
	return rec
}

// deserReq builds a POST request carrying caller + a deserialized DTO in
// context (mirrors what DeserializeJson + JWT auth set up in production).
func deserReq(caller *models.User, deserialized any) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/x", nil)
	ctx := context.WithValue(r.Context(), middleware.UserContextKey, caller)
	ctx = context.WithValue(ctx, middleware.DeserializerContextKey, deserialized)
	return r.WithContext(ctx)
}

// bodyReq builds a POST request carrying caller + a JSON body (handleAdminUpsertRole
// decodes the body directly rather than via the deserializer).
func bodyReq(caller *models.User, body any) *http.Request {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/roles", bytes.NewReader(raw))
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, caller))
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}

// --- HandleAdminSetUserRole (roles.go) --------------------------------------

func TestHandleAdminSetUserRole_Guards(t *testing.T) {
	// A custom catalog for the subset test: "lead" holds only roles.read,
	// "ops" additionally holds users.read (so ops ⊄ lead).
	customCatalog := append(authz.DefaultRoles(),
		models.Role{Key: "lead", Rank: 25, Permissions: []string{string(authz.PermRolesRead)}},
		models.Role{Key: "ops", Rank: 20, Permissions: []string{string(authz.PermRolesRead), string(authz.PermUsersRead)}},
	)

	cases := []struct {
		name       string
		catalog    []models.Role
		callerRole string
		targetRole string // the target user's CURRENT role
		reqRole    string // the role being assigned
		superCount int64  // countActiveSuperusers result
		wantCode   int
	}{
		{"rank climb: assign peer rank", authz.DefaultRoles(), models.RoleKeyApprover, models.RoleKeyUser, models.RoleKeyApprover, 0, apperrors.ErrRoleEscalation.Code},
		{"rank climb: assign superior", authz.DefaultRoles(), models.RoleKeyApprover, models.RoleKeyUser, models.RoleKeySuperuser, 0, apperrors.ErrRoleEscalation.Code},
		{"perm not held by caller", customCatalog, "lead", models.RoleKeyUser, "ops", 0, apperrors.ErrRoleEscalation.Code},
		{"demote last superuser", authz.DefaultRoles(), models.RoleKeySuperuser, models.RoleKeySuperuser, models.RoleKeyUser, 1, apperrors.ErrLastSuperuser.Code},
		{"demote superuser with backup", authz.DefaultRoles(), models.RoleKeySuperuser, models.RoleKeySuperuser, models.RoleKeyUser, 2, http.StatusOK},
		{"unknown target role", authz.DefaultRoles(), models.RoleKeySuperuser, models.RoleKeyUser, "ghost", 0, apperrors.ErrRoleNotFound.Code},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targetID := primitive.NewObjectID()
			withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
				return true, &models.User{ID: targetID, Email: "target@x.com", Role: tc.targetRole}, nil
			})
			withCountSuperusersSeam(t, func(ctx context.Context) (int64, error) { return tc.superCount, nil })

			setCalled := false
			withSetUserRoleSeam(t, func(ctx context.Context, id primitive.ObjectID, role string, expected *time.Time) error {
				setCalled = true
				return nil
			})

			appCtx := guardAppCtx(t, tc.catalog)
			req := deserReq(callerUser(tc.callerRole), adminSetUserRoleRequest{TargetUserID: targetID.Hex(), Role: tc.reqRole})
			rec := serveGuard(appCtx, HandleAdminSetUserRole, req)

			wantStatus(t, rec, tc.wantCode)
			// The write must fire only on the success path, never once a guard trips.
			if wantSuccess := tc.wantCode == http.StatusOK; setCalled != wantSuccess {
				t.Errorf("setUserRole called = %v, want %v", setCalled, wantSuccess)
			}
		})
	}
}

// --- handleAdminUpsertRole (roles.go) ---------------------------------------

func TestHandleAdminUpsertRole_Guards(t *testing.T) {
	leadCatalog := append(authz.DefaultRoles(),
		models.Role{Key: "lead", Rank: 25, Permissions: []string{string(authz.PermRolesRead)}},
	)

	cases := []struct {
		name       string
		catalog    []models.Role
		callerRole string
		req        adminUpsertRoleRequest
		existing   *models.Role // findRoleByKey result (nil = not found)
		wantCode   int
	}{
		{
			name: "superuser-tier perm on editable role", catalog: authz.DefaultRoles(), callerRole: models.RoleKeySuperuser,
			req:      adminUpsertRoleRequest{Key: "newrole", Rank: 10, Permissions: []string{string(authz.PermRolesWrite)}},
			existing: nil, wantCode: apperrors.ErrRoleEscalation.Code,
		},
		{
			name: "new rank >= caller rank", catalog: authz.DefaultRoles(), callerRole: models.RoleKeyApprover,
			req:      adminUpsertRoleRequest{Key: "newrole", Rank: 25, Permissions: []string{string(authz.PermRolesRead)}},
			existing: nil, wantCode: apperrors.ErrRoleEscalation.Code,
		},
		{
			name: "perms not subset of caller", catalog: leadCatalog, callerRole: "lead",
			req:      adminUpsertRoleRequest{Key: "newrole", Rank: 10, Permissions: []string{string(authz.PermUsersRead)}},
			existing: nil, wantCode: apperrors.ErrRoleEscalation.Code,
		},
		{
			name: "immutable role rank edit", catalog: authz.DefaultRoles(), callerRole: models.RoleKeySuperuser,
			req:      adminUpsertRoleRequest{Key: models.RoleKeyUser, Rank: 5, Permissions: []string{}},
			existing: &models.Role{Key: models.RoleKeyUser, Rank: 0, Permissions: []string{}, System: true},
			wantCode: apperrors.ErrImmutableRole.Code,
		},
		{
			name: "mint reserved immutable key", catalog: authz.DefaultRoles(), callerRole: models.RoleKeySuperuser,
			req:      adminUpsertRoleRequest{Key: models.RoleKeySuperuser, Rank: 30, Permissions: []string{authz.Wildcard}},
			existing: nil, wantCode: apperrors.ErrImmutableRole.Code,
		},
		{
			name: "invalid key format", catalog: authz.DefaultRoles(), callerRole: models.RoleKeySuperuser,
			req:      adminUpsertRoleRequest{Key: "Bad Key$", Rank: 10, Permissions: []string{}},
			existing: nil, wantCode: apperrors.ErrInvalidRoleDoc.Code,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFindRoleSeam(t, func(ctx context.Context, key string) (bool, *models.Role, error) {
				if tc.existing == nil {
					return false, nil, nil
				}
				return true, tc.existing, nil
			})
			// Writes must never fire on a guard-tripped path.
			withCreateRoleSeam(t, func(ctx context.Context, r *models.Role) error {
				t.Fatalf("createRole must not run when a guard trips")
				return nil
			})
			withUpdateRoleSeam(t, func(ctx context.Context, r *models.Role, expected *time.Time) error {
				t.Fatalf("updateRole must not run when a guard trips")
				return nil
			})

			appCtx := guardAppCtx(t, tc.catalog)
			rec := serveGuard(appCtx, handleAdminUpsertRole, bodyReq(callerUser(tc.callerRole), tc.req))
			wantStatus(t, rec, tc.wantCode)
		})
	}
}

// TestHandleAdminUpsertRole_ImmutableDescriptionEditable covers V3: the
// description of an immutable role stays editable by a caller at or above its
// rank, but the rank/perms don't, and a caller BELOW the role can't touch it.
func TestHandleAdminUpsertRole_ImmutableDescriptionEditable(t *testing.T) {
	superRow := &models.Role{Key: models.RoleKeySuperuser, Rank: authz.RankSuperuser, Permissions: []string{authz.Wildcard}, System: true}
	withFindRoleSeam(t, func(ctx context.Context, key string) (bool, *models.Role, error) {
		return true, superRow, nil
	})

	t.Run("top-rank caller edits top-role description", func(t *testing.T) {
		withReloadNoop(t)
		updated := false
		withUpdateRoleSeam(t, func(ctx context.Context, r *models.Role, expected *time.Time) error {
			updated = true
			if r.Description != "Updated copy" {
				t.Errorf("description = %q, want %q", r.Description, "Updated copy")
			}
			return nil
		})

		appCtx := guardAppCtx(t, authz.DefaultRoles())
		req := adminUpsertRoleRequest{Key: models.RoleKeySuperuser, Rank: authz.RankSuperuser, Permissions: []string{authz.Wildcard}, Description: "Updated copy"}
		rec := serveGuard(appCtx, handleAdminUpsertRole, bodyReq(callerUser(models.RoleKeySuperuser), req))

		wantStatus(t, rec, http.StatusOK)
		if !updated {
			t.Error("updateRole was not called — description edit did not persist")
		}
	})

	t.Run("lower caller cannot edit superior role description", func(t *testing.T) {
		withUpdateRoleSeam(t, func(ctx context.Context, r *models.Role, expected *time.Time) error {
			t.Fatalf("updateRole must not run for a below-rank caller")
			return nil
		})
		appCtx := guardAppCtx(t, authz.DefaultRoles())
		req := adminUpsertRoleRequest{Key: models.RoleKeySuperuser, Rank: authz.RankSuperuser, Permissions: []string{authz.Wildcard}, Description: "sneaky"}
		rec := serveGuard(appCtx, handleAdminUpsertRole, bodyReq(callerUser(models.RoleKeyApprover), req))
		wantStatus(t, rec, apperrors.ErrRoleEscalation.Code)
	})

	t.Run("rank change on immutable role still blocked", func(t *testing.T) {
		appCtx := guardAppCtx(t, authz.DefaultRoles())
		req := adminUpsertRoleRequest{Key: models.RoleKeySuperuser, Rank: 40, Permissions: []string{authz.Wildcard}, Description: "x"}
		rec := serveGuard(appCtx, handleAdminUpsertRole, bodyReq(callerUser(models.RoleKeySuperuser), req))
		wantStatus(t, rec, apperrors.ErrImmutableRole.Code)
	})
}

// --- HandleAdminDeleteRole (roles.go) ---------------------------------------

func TestHandleAdminDeleteRole_Guards(t *testing.T) {
	cases := []struct {
		name     string
		existing *models.Role
		holders  int64
		wantCode int
	}{
		{"system role undeletable", &models.Role{Key: models.RoleKeyApprover, System: true}, 0, apperrors.ErrImmutableRole.Code},
		{"role in use", &models.Role{Key: "custom", System: false}, 3, apperrors.ErrRoleInUse.Code},
		{"unused custom role deleted", &models.Role{Key: "custom", System: false}, 0, http.StatusOK},
		{"unknown role", nil, 0, apperrors.ErrRoleNotFound.Code},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withReloadNoop(t)
			withFindRoleSeam(t, func(ctx context.Context, key string) (bool, *models.Role, error) {
				if tc.existing == nil {
					return false, nil, nil
				}
				return true, tc.existing, nil
			})
			withCountUsersWithRoleSeam(t, func(ctx context.Context, key string) (int64, error) { return tc.holders, nil })

			deleted := false
			withDeleteRoleSeam(t, func(ctx context.Context, key string) error {
				deleted = true
				return nil
			})

			appCtx := guardAppCtx(t, authz.DefaultRoles())
			req := deserReq(callerUser(models.RoleKeySuperuser), adminDeleteRoleRequest{Key: "custom"})
			rec := serveGuard(appCtx, HandleAdminDeleteRole, req)

			wantStatus(t, rec, tc.wantCode)
			if wantDeleted := tc.wantCode == http.StatusOK; deleted != wantDeleted {
				t.Errorf("deleteRoleByKey called = %v, want %v", deleted, wantDeleted)
			}
		})
	}
}
