package authz

import (
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
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

	if role, ok := roles.ByKey(u.Role); ok {
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
