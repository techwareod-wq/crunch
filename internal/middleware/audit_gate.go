package middleware

// Named audit_gate.go (not audit.go — that file is the admin audit-log
// middleware) — this is the values master switch for the SEO/AEO audit
// surface.

import (
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// auditFeatureEnabled is the boot-injected master switch for the audit
// surface (values.yaml audit.enabled). OFF answers every gated route with a
// 404 masquerade so the dark feature is indistinguishable from an
// unregistered path — while keeping the JSON envelope and CORS headers the
// raw mux 404 would drop (mirrors styleReplicationFeatureEnabled).
var auditFeatureEnabled bool

// SetAuditFeatureEnabled locks the audit-surface switch. Call once at
// startup, before serving.
func SetAuditFeatureEnabled(enabled bool) {
	auditFeatureEnabled = enabled
}

// WithAuditFeature gates a route on the audit-surface switch. Chain it
// between .With(appCtx.Middleware()) and .AllowCORS(), the
// WithStyleReplicationFeature slot — a dark feature answers with zero auth
// work.
func (p pattern) WithAuditFeature() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auditFeatureEnabled {
			SendJSONError(w, r, apperrors.ErrAuditFeatureDisabled)
			return
		}
		ro.ServeHTTP(w, r)
	})
	return p
}
