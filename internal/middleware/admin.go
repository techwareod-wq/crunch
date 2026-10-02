package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// WithAdminAuthorization gates the route on the authenticated caller holding
// the baseline admin.access permission PLUS every `required` route permission,
// resolved from the DB-backed roles cache. It is variadic so route-specific
// requirements compose in one place:
//
//	.WithAdminAuthorization(authz.PermContentManage)   // admin.access + content.manage
//	.WithAdminAuthorization()                          // admin.access only (whoami)
//	.WithAdminAuthorization(authz.PermRolesWrite)      // superuser-tier route
//
// It doubles as the persisted-audit chokepoint (see audit.go): every mutating
// admin request and every denial is written to adminActions. Coupling audit to
// this gate means no admin route can be registered without it — opting out of
// audit would mean opting out of authorization. The audit behavior is UNCHANGED
// by the permission refactor.
//
// Chaining order matters: middlewares wrap outward, so the LAST chained one
// runs FIRST. This must be chained BEFORE .WithJWTAuthentication() so that JWT
// wraps it and has populated UserContextKey by the time it runs:
//
//	middleware.Handle(path, handler).
//	    WithAdminAuthorization(authz.PermUsersRead).  // runs second (inner)
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
		if !adminAuthorized(user, required, time.Now()) {
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

// CallerHasPermission reports whether the authenticated caller holds permission
// p under the SAME rules as the admin gate (DB-resolved permissions). Handlers
// on shared multi-verb paths use it to enforce the write-tier permission the
// path-level gate cannot: the route registry is path-only, so GET and POST on
// /v1/admin/roles share ONE gate, which is
// tagged with the read permission — the handler then requires the write
// permission for mutating methods. Returns false if the auth middleware didn't
// run.
func CallerHasPermission(r *http.Request, p authz.Permission) bool {
	user, ok := r.Context().Value(UserContextKey).(*models.User)
	if !ok {
		return false
	}
	return authz.Has(user, p, rolesCache, time.Now())
}

// adminAuthorized is the gate decision: the caller must hold admin.access plus
// every required permission, DB-resolved from the roles cache. No seeded roles
// (or a nil cache) means no one is authorized — fail closed.
func adminAuthorized(user *models.User, required []authz.Permission, now time.Time) bool {
	if !authz.Has(user, authz.PermAdminAccess, rolesCache, now) {
		return false
	}
	for _, req := range required {
		if !authz.Has(user, req, rolesCache, now) {
			return false
		}
	}
	return true
}
