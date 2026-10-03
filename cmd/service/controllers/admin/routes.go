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
//   - users, access, features, deletion, cron: superuser
//
// The registry is path-only, so verbs can't share a path (hence /delete-style
// suffixes).
func Handle(appCtx *config.AppContext) {
	// --- console ---
	middleware.Handle("/v1/admin/whoami", http.HandlerFunc(HandleAdminWhoami)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/staff", http.HandlerFunc(HandleAdminListStaff)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/audit", http.HandlerFunc(HandleAdminListAuditActions)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- WarehouseHub change log (D-014): full before/after docs ---
	middleware.Handle("/v1/admin/changes", http.HandlerFunc(HandleAdminListChanges)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/changes/detail", http.HandlerFunc(HandleAdminChangeDetail)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- users + access (staff onboarding: they sign in, a superuser sets
	// their access) ---
	middleware.Handle("/v1/admin/users", http.HandlerFunc(HandleAdminListUsers)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/lookup", http.HandlerFunc(HandleAdminLookupUser)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/profile", http.HandlerFunc(HandleAdminGetProfile)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/access", http.HandlerFunc(HandleAdminSetUserAccess)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserAccessRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/features", http.HandlerFunc(HandleAdminSetUserFeatures)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserFeaturesRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/delete", http.HandlerFunc(HandleAdminDeleteUser)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteUserRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- cron (force-run + job listing) ---
	middleware.Handle("/v1/admin/cron/run", http.HandlerFunc(HandleAdminCronRun)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminCronRunRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/cron/jobs", http.HandlerFunc(HandleAdminCronJobs)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
