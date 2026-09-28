package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/models"
)

// withRolesCache installs a roles cache for the gate to resolve against and
// restores the previous one afterward.
func withRolesCache(t *testing.T, roles []models.Role) {
	t.Helper()
	c, err := authz.NewRolesCacheFromRoles(roles)
	if err != nil {
		t.Fatalf("build roles cache: %v", err)
	}
	orig := rolesCache
	SetRolesCache(c)
	t.Cleanup(func() { rolesCache = orig })
}

// requestWithRoleUser builds a request carrying a full user (role + email) in
// context, as JWT auth would.
func requestWithRoleUser(role, email string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	u := &models.User{Role: role, Email: email}
	return r.WithContext(context.WithValue(r.Context(), UserContextKey, u))
}

// buildAdminRouteRBAC registers a route tagged with the given permissions.
func buildAdminRouteRBAC(t *testing.T, path string, perms ...authz.Permission) http.Handler {
	t.Helper()
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached handler"))
	})).WithAdminAuthorization(perms...)
	return routes[path]
}

func TestAdminGate_BaselineAdminAccessPasses(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())
	h := buildAdminRouteRBAC(t, "/test/rbac-baseline")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithRoleUser(models.RoleKeyAdmin, "admin@x.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin on baseline route: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAdminGate_AdminHoldsDomainPermission(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())
	h := buildAdminRouteRBAC(t, "/test/rbac-content", authz.PermContentManage)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithRoleUser(models.RoleKeyAdmin, "admin@x.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin on content.manage route: status = %d, want 200", rec.Code)
	}
}

func TestAdminGate_MissingRequiredPermIsForbiddenAndAudited(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())
	rows := captureAuditInserts(t)
	h := buildAdminRouteRBAC(t, "/test/rbac-superonly", authz.PermRolesWrite)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithRoleUser(models.RoleKeyAdmin, "admin@x.com"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin on roles.write route: status = %d, want 403", rec.Code)
	}
	if len(*rows) != 1 || (*rows)[0].Status != http.StatusForbidden {
		t.Errorf("denial must be audited as a 403 row, got %+v", *rows)
	}
}

func TestAdminGate_SuperuserPassesEverything(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())
	h := buildAdminRouteRBAC(t, "/test/rbac-super-ok", authz.PermPlansWrite, authz.PermUsersDelete)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithRoleUser(models.RoleKeySuperuser, "root@x.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("superuser on superuser-tier route: status = %d, want 200", rec.Code)
	}
}

func TestAdminGate_UserRoleForbidden(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())
	h := buildAdminRouteRBAC(t, "/test/rbac-user-denied")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithRoleUser(models.RoleKeyUser, "customer@x.com"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("customer role on admin route: status = %d, want 403", rec.Code)
	}
}

func TestAdminGate_UnknownAndEmptyRoleForbidden(t *testing.T) {
	withRolesCache(t, authz.DefaultRoles())

	for name, role := range map[string]string{"unknown role": "ghost", "empty role": ""} {
		t.Run(name, func(t *testing.T) {
			h := buildAdminRouteRBAC(t, "/test/rbac-"+role+"-x")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, requestWithRoleUser(role, "x@x.com"))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403 (fail closed)", name, rec.Code)
			}
		})
	}
}
