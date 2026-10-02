package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWithOptionalJWT_AnonymousPaths: no header and a malformed header both
// reach the handler with no user attached — never a 401.
func TestWithOptionalJWT_AnonymousPaths(t *testing.T) {
	const path = "/test/optional-jwt"
	var sawUser bool
	var calls int
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		sawUser = GetOptionalUserFromContext(r) != nil
		w.WriteHeader(http.StatusNoContent)
	})).WithOptionalJWT()
	t.Cleanup(func() { delete(routes, path) })

	for _, auth := range []string{"", "Basic abc", "Bearer"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			r.Header.Set(HeaderAuthorization, auth)
		}
		rec := httptest.NewRecorder()
		routes[path].ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent || sawUser {
			t.Errorf("auth %q: status=%d sawUser=%t, want 204 anonymous", auth, rec.Code, sawUser)
		}
	}
	if calls != 3 {
		t.Errorf("handler calls = %d, want 3", calls)
	}
}
