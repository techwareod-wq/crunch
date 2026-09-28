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

// adminPermissions is the admin bundle (RBAC plan §3): panel access, every
// *.manage domain, comps/overrides, and the read side of the superuser-gated
// catalogs. It deliberately excludes the superuser-tier set (roles.write,
// users.delete, plans.write) — those live ONLY on superuser.
func adminPermissions() []string {
	return []string{
		string(PermAdminAccess),
		string(PermUsersRead),
		string(PermRolesRead),
		string(PermPlansRead),
		string(PermEntitlementsGrant),
		string(PermTrialsManage),
		string(PermContentManage),
		string(PermSiteIntelManage),
		string(PermOnboardingManage),
	}
}

// companyAdminPermissions is the full company-axis bundle (tenancy plan §5.2)
// — what user_admin holds and what the owner rule confers unconditionally.
func companyAdminPermissions() []string {
	out := make([]string, 0, len(AllCompanyPermissions))
	for _, p := range AllCompanyPermissions {
		out = append(out, string(p))
	}
	return out
}

// companyUserPermissions is the member baseline: contribute knowledge, no
// member/billing/settings powers. Data-driven — tune via the roles API, no
// redeploy.
func companyUserPermissions() []string {
	return []string{string(PermCompanyBrainWrite)}
}

// DefaultRoles is the seed catalog: the three global system roles (user /
// admin / superuser) plus the two COMPANY roles (tenancy plan §5.2 —
// user_admin / user_user, resolved per-(user, company) via memberships, never
// assignable as global user.Role, D17). All are System:true, so they cannot
// be deleted; `user` and `superuser` are additionally immutable via the API
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
			Description: "Operations admin. Everything except role assignment, billing/pricing writes, and user deletion.",
			System:      true,
		},
		{
			Key:         models.RoleKeySuperuser,
			Rank:        RankSuperuser,
			Permissions: []string{Wildcard},
			Description: "Full privileges, including role assignment, billing plans, and user deletion. Minted only via rolesmigrate.",
			System:      true,
		},
		{
			Key:         models.CompanyRoleKeyAdmin,
			Rank:        RankUser,
			Permissions: companyAdminPermissions(),
			Description: "Company admin (per-membership role). Full company management; never a global role.",
			System:      true,
		},
		{
			Key:         models.CompanyRoleKeyUser,
			Rank:        RankUser,
			Permissions: companyUserPermissions(),
			Description: "Company member (per-membership role). Baseline access; never a global role.",
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
