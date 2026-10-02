package middleware

import (
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// WithAdminAuthorization gates the route on the authenticated caller having
// panel access (authz.PermAdmin) PLUS every `required` permission (see
// internal/authz):
//
//	.WithAdminAuthorization()                    // any admin (whoami, reads)
//	.WithAdminAuthorization(authz.PermApprover)  // approvers + superusers
//	.WithAdminAuthorization(authz.PermSuperuser) // superusers only
//
// It doubles as the persisted-audit chokepoint (see audit.go): every mutating
// admin request and every denial is written to adminActions. Coupling audit to
// this gate means no admin route can be registered without it — opting out of
// audit would mean opting out of authorization.
//
// Chaining order matters: middlewares wrap outward, so the LAST chained one
// runs FIRST. This must be chained BEFORE .WithJWTAuthentication() so that JWT
// wraps it and has populated UserContextKey by the time it runs:
//
//	middleware.Handle(path, handler).
//	    WithAdminAuthorization(authz.PermEditor).     // runs second (inner)
//	    WithJWTAuthentication().                       // runs first  (outer)
//	    ...
func (p pattern) WithAdminAuthorization(required ...authz.Permission) pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*models.User)
		if !ok {
			// Only possible when this middleware ran before JWT auth — a
			// wiring bug, not an auth failure. Don't mask it as a 401.
			log.Error("admin middleware ran without authenticated user — check middleware chain order", "path", r.URL.Path)
			SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}

		email := strings.ToLower(user.Email)
		if !adminAuthorized(user, required) {
			SendJSONError(w, r, apperrors.ErrAdminAccessRequired)
			// An authenticated non-admin probing the admin surface is a
			// security signal worth keeping. Throttled so a scripted caller
			// can't flood the collection; every denial still logs.
			log.Warn("admin access denied", "email", email, "method", r.Method, "path", r.URL.Path)
			if shouldPersistDenial(email) {
				recordAdminAction(r, email, http.StatusForbidden, nil)
			}
			return
		}

		// One structured line per admin call so the surface is auditable in
		// the existing logs.
		log.Info("admin request", "admin_email", email, "method", r.Method, "path", r.URL.Path)

		if !auditsMutation(r.Method) {
			ro.ServeHTTP(w, r)
			return
		}

		captured := captureAuditBody(r)
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if panicked := recover(); panicked != nil {
				// Record the action before the panic propagates — a crash
				// mid-mutation is exactly what the trail must not lose.
				recordAdminAction(r, email, http.StatusInternalServerError, auditPayload(r, captured))
				panic(panicked)
			}
		}()
		ro.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		recordAdminAction(r, email, status, auditPayload(r, captured))
	})
	return p
}

// adminAuthorized is the gate decision: panel access plus every required
// permission. Fails closed.
func adminAuthorized(user *models.User, required []authz.Permission) bool {
	if !authz.Has(user, authz.PermAdmin) {
		return false
	}
	for _, req := range required {
		if !authz.Has(user, req) {
			return false
		}
	}
	return true
}
