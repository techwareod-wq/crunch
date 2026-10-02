package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the platform /v1/admin/* routes (feature controllers register
// their own). Every route chains WithAdminAuthorization inside
// WithJWTAuthentication (last chained runs first, so JWT populates the user
// before the permission check) and is tagged with what it needs on top of
// panel access (internal/authz):
//
//   - whoami, staff list: any admin
//   - audit trail + change log: approver
//   - users, access, deletion, cron: superuser
//
// The registry is path-only, so verbs can't share a path (hence /delete-style
// suffixes).
func Handle(appCtx *config.AppContext) {
	route := func(path string, h http.HandlerFunc, method string, body func(http.Handler) http.Handler, required ...authz.Permission) {
		p := middleware.Handle(path, h).
			WithAdminAuthorization(required...).
			WithJWTAuthentication()
		if body != nil {
			p = p.With(body)
		}
		p.WithMethods(method).
			With(appCtx.Middleware()).
			AllowCORS().
			WithLogEnabled()
	}

	// --- console ---
	route("/v1/admin/whoami", HandleAdminWhoami, http.MethodGet, nil)
	route("/v1/admin/staff", HandleAdminListStaff, http.MethodGet, nil)
	route("/v1/admin/audit", HandleAdminListAuditActions, http.MethodGet, nil, authz.PermApprover)

	// --- WarehouseHub change log (D-014): full before/after docs ---
	route("/v1/admin/changes", HandleAdminListChanges, http.MethodGet, nil, authz.PermApprover)
	route("/v1/admin/changes/detail", HandleAdminChangeDetail, http.MethodGet, nil, authz.PermApprover)

	// --- users + access (staff onboarding: they sign in, a superuser sets
	// their access) ---
	route("/v1/admin/users", HandleAdminListUsers, http.MethodGet, nil, authz.PermSuperuser)
	route("/v1/admin/users/lookup", HandleAdminLookupUser, http.MethodGet, nil, authz.PermSuperuser)
	route("/v1/admin/users/profile", HandleAdminGetProfile, http.MethodGet, nil, authz.PermSuperuser)
	route("/v1/admin/users/access", HandleAdminSetUserAccess, http.MethodPost,
		middleware.DeserializeJson[adminSetUserAccessRequest](), authz.PermSuperuser)
	route("/v1/admin/users/delete", HandleAdminDeleteUser, http.MethodPost,
		middleware.DeserializeJson[adminDeleteUserRequest](), authz.PermSuperuser)

	// --- cron (force-run + job listing) ---
	route("/v1/admin/cron/run", HandleAdminCronRun, http.MethodPost,
		middleware.DeserializeJson[adminCronRunRequest](), authz.PermSuperuser)
	route("/v1/admin/cron/jobs", HandleAdminCronJobs, http.MethodGet, nil, authz.PermSuperuser)
}
