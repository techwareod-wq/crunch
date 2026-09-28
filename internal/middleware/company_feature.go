package middleware

import (
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// companyFeatureEnabled is the boot-injected master switch for the user-facing
// company surface (values.yaml company.enabled). OFF answers every gated route
// with a 404 masquerade so the dark feature is indistinguishable from an
// unregistered path — while keeping the JSON envelope and CORS headers the raw
// mux 404 would drop. The data layer underneath (personal-company minting,
// Paddle webhook seat sync, seat entitlement projection) is NOT gated by this.
var companyFeatureEnabled bool

// SetCompanyFeatureEnabled locks the company-surface switch. Call once at
// startup, before serving (mirrors SetPlansCache).
func SetCompanyFeatureEnabled(enabled bool) {
	companyFeatureEnabled = enabled
}

// WithCompanyFeature gates a route on the company-surface switch.
//
// Chaining order matters: middlewares wrap outward, so the LAST chained one
// runs FIRST. Chain this between .With(appCtx.Middleware()) and .AllowCORS()
// so it executes after the CORS/method wrappers but before JWT — a dark
// feature answers with zero auth work and no user lookup:
//
//	middleware.Handle(path, handler).
//	    WithJWTAuthentication().       // runs after the gate
//	    With(appCtx.Middleware()).
//	    WithCompanyFeature().          // runs right after CORS
//	    AllowCORS().
//	    ...
func (p pattern) WithCompanyFeature() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !companyFeatureEnabled {
			SendJSONError(w, r, apperrors.ErrCompanyFeatureDisabled)
			return
		}
		ro.ServeHTTP(w, r)
	})
	return p
}
