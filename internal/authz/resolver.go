package authz

import (
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// EffectivePermissions computes the caller's live permission set:
//
//	role.Permissions (expand "*") + extra_grants − extra_revokes
//
// honoring per-entry expiry. Pure in-memory — the user doc is already loaded by
// jwt.go and roles are cached, so this costs zero DB reads. now is injected for
// tests.
//
// Fails closed like the entitlements resolver: a nil user, an unknown/empty
// role, or a nil cache yields no role-derived permissions. Grants are still
// applied (they can never carry admin.access or superuser-tier keys — the API
// rejects those — so a grant on a role-less user is inert: it can add a
// capability but never panel entry).
func EffectivePermissions(u *models.User, roles *RolesCache, now time.Time) map[Permission]bool {
	set := map[Permission]bool{}
	if u == nil {
		return set
	}

	// A company role key as a global user.Role resolves NOTHING (D17): the
	// assign endpoint rejects them, and this guard makes a hand-edited doc
	// structurally inert too. (Even without it, company keys fail
	// IsKnownPermission below — this is belt and suspenders.)
	if role, ok := roles.ByKey(u.Role); ok && !IsCompanyRoleKey(u.Role) {
		for _, p := range role.Permissions {
			if p == Wildcard {
				for _, all := range AllPermissions {
					set[all] = true
				}
				continue
			}
			if IsKnownPermission(p) {
				set[Permission(p)] = true
			}
			// Unknown keys in a role doc are tolerated (forward compat) and
			// ignored — the cache already rejects them at load, so this only
			// matters for a nil-cache path.
		}
	}

	for _, g := range u.ExtraGrants {
		if g.ExpiresAt != nil && !now.Before(*g.ExpiresAt) {
			continue
		}
		if IsKnownPermission(g.Key) {
			set[Permission(g.Key)] = true
		}
	}
	for _, r := range u.ExtraRevokes {
		if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			continue
		}
		delete(set, Permission(r.Key))
	}
	return set
}

// Has reports whether the caller holds permission p. Nil-cache / unknown-role
// tolerant — fails closed.
func Has(u *models.User, p Permission, roles *RolesCache, now time.Time) bool {
	return EffectivePermissions(u, roles, now)[p]
}

// Rank returns the caller's role rank — the assignment ceiling ONLY. It never
// grants route access: a rank-30 role with an empty permission list has no
// access (the resolver reads Permissions, not Rank). An unknown or empty role,
// or a nil cache/user, is rank 0 (fail closed).
func Rank(u *models.User, roles *RolesCache) int {
	if u == nil {
		return 0
	}
	if role, ok := roles.ByKey(u.Role); ok {
		return role.Rank
	}
	return 0
}

// EffectivePermissionKeys returns the caller's effective permissions as a
// sorted string slice (for whoami / DTOs). "*" is expanded — the wire always
// carries concrete keys so the dashboard can gate menu items by capability.
func EffectivePermissionKeys(u *models.User, roles *RolesCache, now time.Time) []string {
	set := EffectivePermissions(u, roles, now)
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// ExpandRolePermissions is the concrete permission set a role confers, with "*"
// expanded. Used by the grant-only-what-you-hold guard on role assignment.
func ExpandRolePermissions(role *models.Role) map[Permission]bool {
	set := map[Permission]bool{}
	if role == nil {
		return set
	}
	for _, p := range role.Permissions {
		if p == Wildcard {
			for _, all := range AllPermissions {
				set[all] = true
			}
			continue
		}
		if IsKnownPermission(p) {
			set[Permission(p)] = true
		}
	}
	return set
}

// ExpandKeys expands an arbitrary permission-key list (e.g. a proposed catalog
// role's Permissions) into a concrete set, dropping unknown keys. Used by the
// grant-only-what-you-hold guard on catalog upserts.
func ExpandKeys(keys []string) map[Permission]bool {
	set := map[Permission]bool{}
	for _, p := range keys {
		if p == Wildcard {
			for _, all := range AllPermissions {
				set[all] = true
			}
			continue
		}
		if IsKnownPermission(p) {
			set[Permission(p)] = true
		}
	}
	return set
}

// IsSubset reports whether every permission in sub is also in super — the
// "grant only what you hold" primitive. Both sets are already "*"-expanded, so
// this is a plain membership check, never a string compare against "*".
func IsSubset(sub, super map[Permission]bool) bool {
	for p := range sub {
		if !super[p] {
			return false
		}
	}
	return true
}

// EffectiveCompanyPermissions computes a member's live COMPANY permission set
// from their membership's role (tenancy plan §5.3). The company axis is fully
// separate from the global resolver (D17): no extra-grants, no wildcard, no
// platform-admin bypass — cross-tenant access is a hard no.
//
// Fails closed INSIDE the resolver unless the membership is claimed:
// archived/invited memberships yield the empty set regardless of what a call
// site forgot to check (D13). A membership pointing at a non-company role key
// also resolves nothing.
func EffectiveCompanyPermissions(m *models.CompanyMembership, roles *RolesCache) map[Permission]bool {
	set := map[Permission]bool{}
	if m == nil || m.Status != models.CompanyMembershipStatusClaimed {
		return set
	}
	if !IsCompanyRoleKey(m.Role) {
		return set
	}
	role, ok := roles.ByKey(m.Role)
	if !ok {
		return set
	}
	for _, p := range role.Permissions {
		if IsKnownCompanyPermission(p) {
			set[Permission(p)] = true
		}
	}
	return set
}

// AllCompanyPermissionSet is the full company-axis grant — what the owner
// rule confers.
func AllCompanyPermissionSet() map[Permission]bool {
	set := make(map[Permission]bool, len(AllCompanyPermissions))
	for _, p := range AllCompanyPermissions {
		set[p] = true
	}
	return set
}

// ResolveCompanyPermissions is the request-time company resolver: the owner
// rule (D12/D21 — OwnerUserID match ⇒ user_admin perms unconditionally, no
// catalog dependency) layered over EffectiveCompanyPermissions. userID is the
// caller; company may be nil (resolves via membership only).
func ResolveCompanyPermissions(userID primitive.ObjectID, company *models.Company, m *models.CompanyMembership, roles *RolesCache) map[Permission]bool {
	if company != nil && !userID.IsZero() && company.OwnerUserID == userID {
		return AllCompanyPermissionSet()
	}
	return EffectiveCompanyPermissions(m, roles)
}

// CompanyHas reports whether the resolved company permission set holds p —
// the ONLY way company routes gate (never the global Has, D17).
func CompanyHas(perms map[Permission]bool, p Permission) bool {
	return perms[p]
}
