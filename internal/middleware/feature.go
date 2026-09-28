package middleware

import (
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/entitlements"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// WithFeature is the single per-route authorization gate. It gates the route
// on the feature being in the caller's EFFECTIVE set for the app: plan feature
// list (trial list while trialing) plus live grants minus live revokes.
//
// An empty feature ("") is the subscription-only (app-level) gate: it requires
// a valid subscription for the app and returns subscription_required
// otherwise, without touching plan data — the check read/support routes use.
//
// Chaining order matters: middlewares wrap outward, so the LAST chained one
// runs FIRST. This must be chained BEFORE .WithJWTAuthentication() so that JWT
// wraps it and has populated UserContextKey by the time it runs:
//
//	middleware.Handle(path, handler).
//	    WithFeature(models.AppIDIndexly, feature).  // runs second (inner)
//	    WithJWTAuthentication().                    // runs first  (outer)
//	    ...
func (p pattern) WithFeature(app string, feature entitlements.Feature) pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*models.User)
		if !ok {
			log.Error("feature middleware ran without authenticated user — check middleware chain order",
				"path", r.URL.Path, "app", app, "feature", string(feature))
			SendJSONError(w, r, apperrors.ErrSubscriptionCheckFailed)
			return
		}

		switch entitlements.Resolve(user, app, feature, plansCache, time.Now().UTC()) {
		case entitlements.Allow:
			ro.ServeHTTP(w, r)
		case entitlements.FeatureNotIncluded:
			SendJSONError(w, r, apperrors.FeatureNotIncluded(app, string(feature)))
		default:
			SendJSONError(w, r, apperrors.ErrSubscriptionRequired)
		}
	})
	return p
}
