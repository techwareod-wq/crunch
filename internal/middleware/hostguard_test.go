package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestHostGuard(t *testing.T) {
	tests := []struct {
		name     string
		hosts    []string
		reqHost  string
		path     string
		wantCode int
	}{
		{"disabled allows any host", nil, "1.2.3.4:3090", "/v1/users", http.StatusOK},
		{"allowed domain passes", []string{"api.example.com"}, "api.example.com", "/v1/users", http.StatusOK},
		{"allowed domain with port passes", []string{"api.example.com"}, "api.example.com:3090", "/v1/users", http.StatusOK},
		{"case-insensitive match", []string{"api.example.com"}, "API.Example.com", "/v1/users", http.StatusOK},
		{"direct IP rejected", []string{"api.example.com"}, "1.2.3.4:3090", "/v1/users", http.StatusForbidden},
		{"other domain rejected", []string{"api.example.com"}, "evil.example.com", "/v1/users", http.StatusForbidden},
		{"loopback IP allowed (healthcheck)", []string{"api.example.com"}, "127.0.0.1:3090", "/health", http.StatusOK},
		{"loopback IP allowed on any path", []string{"api.example.com"}, "127.0.0.1:3090", "/v1/users", http.StatusOK},
		{"health path exempt regardless of host", []string{"api.example.com"}, "10.0.0.5:3090", "/health", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetAllowedHosts(tt.hosts)
			t.Cleanup(func() { SetAllowedHosts(nil) })

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.reqHost+tt.path, nil)
			req.Host = tt.reqHost
			rec := httptest.NewRecorder()

			HostGuard(okHandler()).ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("Host %q path %q: got %d, want %d", tt.reqHost, tt.path, rec.Code, tt.wantCode)
			}
		})
	}
}
