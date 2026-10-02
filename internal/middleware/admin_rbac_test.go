package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/models"
)

// requestWithAccess builds a request carrying a user (role + permissions) in
// context, as JWT auth would.
func requestWithAccess(role, email string, perms ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	u := &models.User{Role: role, Email: email, Permissions: perms}
	return r.WithContext(context.WithValue(r.Context(), UserContextKey, u))
}

// buildAdminRoute registers a route tagged with the given permissions.
func buildGateRoute(t *testing.T, path string, perms ...authz.Permission) http.Handler {
	t.Helper()
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached handler"))
	})).WithAdminAuthorization(perms...)
	return routes[path]
}

func TestAdminGate(t *testing.T) {
	captureAuditInserts(t)
	cases := []struct {
		name  string
		perms []authz.Permission
		req   *http.Request
		want  int
	}{
		{"admin on baseline", nil, requestWithAccess(models.RoleAdmin, "a@x.com"), http.StatusOK},
		{"editor on editor route", []authz.Permission{authz.PermEditor}, requestWithAccess(models.RoleAdmin, "a@x.com", "editor"), http.StatusOK},
		{"editor on approver route", []authz.Permission{authz.PermApprover}, requestWithAccess(models.RoleAdmin, "a@x.com", "editor"), http.StatusForbidden},
		{"approver on superuser route", []authz.Permission{authz.PermSuperuser}, requestWithAccess(models.RoleAdmin, "a@x.com", "editor", "approver"), http.StatusForbidden},
		{"superuser passes everything", []authz.Permission{authz.PermSuperuser, authz.PermApprover}, requestWithAccess(models.RoleSuperuser, "s@x.com"), http.StatusOK},
		{"user role forbidden", nil, requestWithAccess(models.RoleUser, "u@x.com", "editor"), http.StatusForbidden},
		{"unknown role forbidden", nil, requestWithAccess("approver", "u@x.com"), http.StatusForbidden},
		{"empty role forbidden", nil, requestWithAccess("", "u@x.com"), http.StatusForbidden},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := buildGateRoute(t, "/test/gate/"+string(rune('a'+i)), c.perms...)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, c.req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestAdminGate_DenialIsAudited(t *testing.T) {
	rows := captureAuditInserts(t)
	h := buildGateRoute(t, "/test/gate-audit", authz.PermSuperuser)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithAccess(models.RoleAdmin, "denied-audit@x.com", "approver"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(*rows) != 1 || (*rows)[0].Status != http.StatusForbidden {
		t.Errorf("denial must be audited as a 403 row, got %+v", *rows)
	}
}
