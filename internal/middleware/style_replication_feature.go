package middleware

import (
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// styleReplicationFeatureEnabled is the boot-injected master switch for the
// style replication surface (values.yaml styleReplication.enabled). OFF
// answers every gated route with a 404 masquerade so the dark feature is
// indistinguishable from an unregistered path — while keeping the JSON
// envelope and CORS headers the raw mux 404 would drop (mirrors
// analyticsFeatureEnabled).
var styleReplicationFeatureEnabled bool

// SetStyleReplicationFeatureEnabled locks the style-replication-surface
// switch. Call once at startup, before serving (mirrors
// SetAnalyticsFeatureEnabled).
func SetStyleReplicationFeatureEnabled(enabled bool) {
	styleReplicationFeatureEnabled = enabled
}

// WithStyleReplicationFeature gates a route on the style-replication-surface
// switch. Chain it between .With(appCtx.Middleware()) and .AllowCORS(), same
// slot as WithAnalyticsFeature — a dark feature answers with zero auth work.
func (p pattern) WithStyleReplicationFeature() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !styleReplicationFeatureEnabled {
			SendJSONError(w, r, apperrors.ErrStyleReplicationFeatureDisabled)
			return
		}
		ro.ServeHTTP(w, r)
	})
	return p
}
