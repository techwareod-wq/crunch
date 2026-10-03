package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/models"
)

func TestWithFeature(t *testing.T) {
	Handle("/test/feature", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).WithFeature(authz.FeatureSearch)
	h := routes["/test/feature"]

	cases := []struct {
		name   string
		user   *models.User
		status int
		code   string
	}{
		{"no user in context is a wiring bug", nil, http.StatusInternalServerError, ""},
		{"no access at all", &models.User{Role: models.RoleUser}, http.StatusForbidden, "access_required"},
		{"other feature only", &models.User{Role: models.RoleUser, Features: []string{"enquiries"}}, http.StatusForbidden, "feature_not_included"},
		{"has the feature", &models.User{Role: models.RoleUser, Features: []string{"search"}}, http.StatusOK, ""},
		{"staff", &models.User{Role: models.RoleAdmin}, http.StatusOK, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/test/feature", nil)
		if c.user != nil {
			req = req.WithContext(context.WithValue(req.Context(), UserContextKey, c.user))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.status)
			continue
		}
		if c.code != "" {
			var body map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if got, _ := body["code"].(string); got != c.code {
				t.Errorf("%s: body = %s, want code %q", c.name, rec.Body.String(), c.code)
			}
		}
	}
}
