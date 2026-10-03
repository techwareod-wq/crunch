package middleware

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// WithFeature is the per-route gate for visitor routes: the caller must hold
// the feature (models.User.Features, set by a superuser; staff hold every
// feature — see authz.HasFeature).
//
// authz.FeatureAccess ("") is the "any access" gate: the caller needs at
// least one granted feature, and gets access_required otherwise. A caller
// with some access but not this feature gets feature_not_included.
//
// Chaining order matters: middlewares wrap outward, so the LAST chained one
// runs FIRST. This must be chained BEFORE .WithJWTAuthentication() so that JWT
// wraps it and has populated UserContextKey by the time it runs:
//
//	middleware.Handle(path, handler).
//	    WithFeature(authz.FeatureSearch).  // runs second (inner)
//	    WithJWTAuthentication().           // runs first  (outer)
//	    ...
func (p pattern) WithFeature(feature authz.Feature) pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*models.User)
		if !ok {
			log.Error("feature middleware ran without authenticated user — check middleware chain order",
				"path", r.URL.Path, "feature", string(feature))
			SendJSONError(w, r, apperrors.ErrFeatureCheckFailed)
			return
		}

		switch {
		case authz.HasFeature(user, feature):
			ro.ServeHTTP(w, r)
		case authz.HasFeature(user, authz.FeatureAccess):
			SendJSONError(w, r, apperrors.FeatureNotIncluded(string(feature)))
		default:
			SendJSONError(w, r, apperrors.ErrAccessRequired)
		}
	})
	return p
}
