// Package authz owns role-based authorization for the /v1/admin surface: the
// typed permission keys, the roles cache (role key → role/permissions
// resolution), and the request-time resolver + escalation guards. It sits
// between models (storage) and middleware (enforcement).
package authz

// Permission is a permission key. Typed constants are the only way routes
// reference permissions; role docs and per-user grants store these strings.
// Unknown strings in a role doc are tolerated by the resolver (forward compat),
// but admin writes validate against the known set — a typo'd grant that
// silently no-ops would leave staff without access they think they have.
type Permission string

const (
	PermAdminAccess   Permission = "admin.access"   // baseline: reach the panel at all
	PermUsersRead     Permission = "users.read"     // list/lookup/profile users
	PermUsersDelete   Permission = "users.delete"   // superuser: delete/deactivate users
	PermRolesRead     Permission = "roles.read"     // list the role catalog
	PermRolesWrite    Permission = "roles.write"    // superuser: edit catalog + assign roles
	PermCronManage    Permission = "cron.manage"    // force-run cron jobs
	PermMigrationsRun Permission = "migrations.run" // superuser: run data migrations from the panel
)

// Wildcard grants every permission (superuser). It is legal ONLY in
// Role.Permissions — never in extra_grants/extra_revokes — and is always
// EXPANDED (never string-compared) in subset/containment checks.
const Wildcard = "*"

// AllPermissions is the full catalog: the expansion of "*" and the validation
// set. Extend here when a new route domain is added.
var AllPermissions = []Permission{
	PermAdminAccess,
	PermUsersRead,
	PermUsersDelete,
	PermRolesRead,
	PermRolesWrite,
	PermCronManage,
	PermMigrationsRun,
}

var knownPermissions = func() map[string]bool {
	m := make(map[string]bool, len(AllPermissions))
	for _, p := range AllPermissions {
		m[string(p)] = true
	}
	return m
}()

// superuserTier is the set of permissions that may live ONLY on the immutable
// superuser role: they are rejected in extra_grants AND in catalog upserts of
// any editable role, which makes superuser powers structurally non-delegable
// (RBAC plan §7.6). "*" is superuser-tier by definition (it contains them).
var superuserTier = map[Permission]bool{
	PermRolesWrite:  true,
	PermUsersDelete: true,
	// cron.manage sits here because a force-run fans work out to OTHER users'
	// accounts with no per-target audit trail — a platform-level trigger, not a
	// support capability.
	PermCronManage: true,
	// migrations.run can touch every user doc and drop an index —
	// a platform-level lever, not a support capability.
	PermMigrationsRun: true,
}

// IsKnownPermission reports whether key is a defined permission constant.
// "*" is NOT a known permission (it is a wildcard, valid only in a role's
// permission list) — callers that accept it must check Wildcard explicitly.
func IsKnownPermission(key string) bool {
	return knownPermissions[key]
}

// IsSuperuserTier reports whether key is a superuser-only permission — the
// tier keys or the wildcard that expands to them.
func IsSuperuserTier(key string) bool {
	return key == Wildcard || superuserTier[Permission(key)]
}

// IsValidRolePermission reports whether key may appear in a Role.Permissions
// list: a known permission OR the wildcard.
func IsValidRolePermission(key string) bool {
	return key == Wildcard || IsKnownPermission(key)
}
