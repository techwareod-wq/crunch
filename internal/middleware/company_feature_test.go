package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func buildCompanyFeatureRoute(t *testing.T, path string) http.Handler {
	t.Helper()
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached handler"))
	})).WithCompanyFeature()
	return routes[path]
}

func withCompanyFeature(t *testing.T, enabled bool) {
	t.Helper()
	orig := companyFeatureEnabled
	SetCompanyFeatureEnabled(enabled)
	t.Cleanup(func() { companyFeatureEnabled = orig })
}

func TestWithCompanyFeature_EnabledPassesThrough(t *testing.T) {
	withCompanyFeature(t, true)
	h := buildCompanyFeatureRoute(t, "/test/company-feature-on")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "reached handler" {
		t.Fatalf("enabled: status = %d body = %q, want 200 %q", rec.Code, rec.Body.String(), "reached handler")
	}
}

func TestWithCompanyFeature_DisabledIs404Masquerade(t *testing.T) {
	withCompanyFeature(t, false)
	h := buildCompanyFeatureRoute(t, "/test/company-feature-off")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "reached handler") {
		t.Fatalf("disabled: handler must not run, got body: %s", body)
	}
	// The masquerade must not name the feature — "not found" and nothing else.
	if strings.Contains(strings.ToLower(body), "company") {
		t.Errorf("404 body leaks the feature name: %s", body)
	}
}
