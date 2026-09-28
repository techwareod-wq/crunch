package middleware

import (
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// analyticsFeatureEnabled is the boot-injected master switch for the analytics
// surface (values.yaml gsc.enabled). OFF answers every gated route with a 404
// masquerade so the dark feature is indistinguishable from an unregistered
// path — while keeping the JSON envelope and CORS headers the raw mux 404
// would drop. The ingest layer underneath (cron beats, orchestrator) is gated
// separately by the cron.jobs.analytics_* flags, not by this.
var analyticsFeatureEnabled bool

// SetAnalyticsFeatureEnabled locks the analytics-surface switch. Call once at
// startup, before serving (mirrors SetCompanyFeatureEnabled).
func SetAnalyticsFeatureEnabled(enabled bool) {
	analyticsFeatureEnabled = enabled
}

// WithAnalyticsFeature gates a route on the analytics-surface switch. Chain it
// between .With(appCtx.Middleware()) and .AllowCORS(), same slot as
// WithCompanyFeature — a dark feature answers with zero auth work.
func (p pattern) WithAnalyticsFeature() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !analyticsFeatureEnabled {
			SendJSONError(w, r, apperrors.ErrAnalyticsFeatureDisabled)
			return
		}
		ro.ServeHTTP(w, r)
	})
	return p
}
