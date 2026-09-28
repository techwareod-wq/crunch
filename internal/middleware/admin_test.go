package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// buildAdminRoute assembles a route the way production code does — handler
// first, then .WithAdminAuthorization().WithJWTAuthentication() ordering is
// emulated by injecting the user into context before the guard runs.
func buildAdminRoute(t *testing.T, path string) http.Handler {
	t.Helper()
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached handler"))
	})).WithAdminAuthorization()
	return routes[path]
}

func TestWithAdminAuthorization_MissingUserIsWiringBug500(t *testing.T) {
	// No user in context means the guard ran before JWT auth — a chain-order
	// bug that must surface as a 500, not masquerade as 401/403.
	h := buildAdminRoute(t, "/test/admin-no-user")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing user: status = %d, want 500", rec.Code)
	}
}
