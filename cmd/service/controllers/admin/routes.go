package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the /v1/admin/* routes. Every route chains
// WithAdminAuthorization inside WithJWTAuthentication (last chained runs first,
// so JWT populates the user before the permission check). Admin-triggered work
// shares the global tokentracker LLM budget because the same services and
// queues are reused.
//
// Every route is tagged with its domain permission (RBAC plan §3/§5) — the
// baseline admin.access is always required, plus each tagged permission. This
// is what makes future custom (e.g. support-tier) roles enforceable. Only
// whoami and the audit list are argless (baseline only). Shared multi-verb
// paths (roles) are tagged with the READ permission and re-check the
// WRITE permission in-handler (the registry is path-only).
//
// Mirrors keep the same /delete, /edit suffix workarounds as the user routes —
// the registry is path-only, so verbs can't share a path.
func Handle(appCtx *config.AppContext) {
	// --- console (acting-admin identity + persisted audit trail) ---

	middleware.Handle("/v1/admin/whoami", http.HandlerFunc(HandleAdminWhoami)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/audit", http.HandlerFunc(HandleAdminListAuditActions)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- roles & permissions (RBAC) ---
	// GET lists (roles.read); POST upserts (roles.write, re-checked in-handler
	// since the path-only registry can't gate GET and POST differently).

	middleware.Handle("/v1/admin/roles", http.HandlerFunc(HandleAdminRoles)).
		WithAdminAuthorization(authz.PermRolesRead).
		WithJWTAuthentication().
		WithMethods("GET", "POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/roles/delete", http.HandlerFunc(HandleAdminDeleteRole)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteRoleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Catalog seed — the API twin of `rolesmigrate -seed-roles` (dry-run
	// default).
	middleware.Handle("/v1/admin/roles/seed", http.HandlerFunc(HandleAdminSeedRoles)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJsonOptional[adminSeedRolesRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- users (dashboard entry points + profile + role/grants management) ---

	middleware.Handle("/v1/admin/users", http.HandlerFunc(HandleAdminListUsers)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/lookup", http.HandlerFunc(HandleAdminLookupUser)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/profile", http.HandlerFunc(HandleAdminGetProfile)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/role", http.HandlerFunc(HandleAdminSetUserRole)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserRoleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/grants", http.HandlerFunc(HandleAdminSetUserGrants)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserGrantsRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/delete", http.HandlerFunc(HandleAdminDeleteUser)).
		WithAdminAuthorization(authz.PermUsersDelete).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteUserRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- cron (force-run + job listing) ---

	middleware.Handle("/v1/admin/cron/run", http.HandlerFunc(HandleAdminCronRun)).
		WithAdminAuthorization(authz.PermCronManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminCronRunRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/cron/jobs", http.HandlerFunc(HandleAdminCronJobs)).
		WithAdminAuthorization(authz.PermCronManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
