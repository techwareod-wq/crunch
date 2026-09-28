package authz

import "github.com/atharva-ng/crunch/internal/models"

// Ranks for the three seeded system roles. The gap at 10 is deliberate room
// for a future support-style role (created via the API, no redeploy) — its
// route tags are already enforceable.
const (
	RankUser      = 0
	RankAdmin     = 20
	RankSuperuser = 30
)

// adminPermissions is the admin bundle: panel access plus the read side of the
// superuser-gated catalogs. It deliberately excludes the superuser-tier set
// (roles.write, users.delete, cron.manage, migrations.run) — those live ONLY
// on superuser. Add each new *.manage domain here.
func adminPermissions() []string {
	return []string{
		string(PermAdminAccess),
		string(PermUsersRead),
		string(PermRolesRead),
	}
}

// DefaultRoles is the seed catalog: the three global system roles (user /
// admin / superuser). All are System:true, so they cannot be deleted; `user` and `superuser` are additionally immutable via the API
// (enforced in the handler). Consumed by cmd/rolesmigrate -seed-roles.
func DefaultRoles() []models.Role {
	return []models.Role{
		{
			Key:         models.RoleKeyUser,
			Rank:        RankUser,
			Permissions: []string{}, // no admin access — the customer default
			Description: "Standard customer. No admin panel access.",
			System:      true,
		},
		{
			Key:         models.RoleKeyAdmin,
			Rank:        RankAdmin,
			Permissions: adminPermissions(),
			Description: "Operations admin. Everything except role assignment, cron force-runs, migrations, and user deletion.",
			System:      true,
		},
		{
			Key:         models.RoleKeySuperuser,
			Rank:        RankSuperuser,
			Permissions: []string{Wildcard},
			Description: "Full privileges, including role assignment and user deletion. Minted only via rolesmigrate.",
			System:      true,
		},
	}
}

// IsImmutableSystemRole reports whether a role key is frozen against API edits
// to rank/permissions (RBAC plan §7.5): `user` and `superuser`. `admin` is a
// system role too but stays API-tunable (guarded). Description edits are still
// allowed on immutable roles.
func IsImmutableSystemRole(key string) bool {
	return key == models.RoleKeyUser || key == models.RoleKeySuperuser
}
