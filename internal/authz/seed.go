package authz

import "github.com/atharva-ng/crunch/internal/models"

// Ranks for the seeded system roles (D-013). editor < approver < superuser;
// `user` has no panel access.
const (
	RankUser      = 0
	RankEditor    = 10
	RankApprover  = 20
	RankSuperuser = 30
)

// editorPermissions is the editor bundle: panel access, listing edits, needs-info
// answers and the enquiry inbox. No approve/archive, no tree edits, no audit.
func editorPermissions() []string {
	return []string{
		string(PermAdminAccess),
		string(PermWarehousesRead),
		string(PermWarehousesEdit),
		string(PermAnswersWrite),
		string(PermEnquiriesRead),
		string(PermEnquiriesWrite),
	}
}

// approverPermissions is the approver bundle (replaces crunch's `admin`): the
// editor bundle plus approvals, archive, tree/industry edits, audit, analytics
// and the read side of the superuser-gated catalogs. It deliberately excludes
// the superuser-tier set (roles.write, users.delete, cron.manage,
// migrations.run, staff.invite) — those live ONLY on superuser.
func approverPermissions() []string {
	return append(editorPermissions(),
		string(PermWarehousesApprove),
		string(PermWarehousesArchive),
		string(PermAttributesManage),
		string(PermIndustriesManage),
		string(PermAuditRead),
		string(PermAnalyticsRead),
		string(PermUsersRead),
		string(PermRolesRead),
	)
}

// DefaultRoles is the seed catalog: the four global system roles (user /
// editor / approver / superuser). All are System:true, so they cannot be
// deleted; `user` and `superuser` are additionally immutable via the API
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
			Key:         models.RoleKeyEditor,
			Rank:        RankEditor,
			Permissions: editorPermissions(),
			Description: "Content editor. Edits listing drafts and answers, works the enquiry inbox. Cannot approve.",
			System:      true,
		},
		{
			Key:         models.RoleKeyApprover,
			Rank:        RankApprover,
			Permissions: approverPermissions(),
			Description: "Approver. Everything an editor does plus approvals, archive, attribute/industry rules, audit and analytics.",
			System:      true,
		},
		{
			Key:         models.RoleKeySuperuser,
			Rank:        RankSuperuser,
			Permissions: []string{Wildcard},
			Description: "Full privileges, including role assignment, staff invites and user deletion. Minted only via rolesmigrate.",
			System:      true,
		},
	}
}

// IsImmutableSystemRole reports whether a role key is frozen against API edits
// to rank/permissions (RBAC plan §7.5): `user` and `superuser`. editor and
// approver are system roles too but stay API-tunable (guarded). Description
// edits are still allowed on immutable roles.
func IsImmutableSystemRole(key string) bool {
	return key == models.RoleKeyUser || key == models.RoleKeySuperuser
}
