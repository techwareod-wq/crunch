package authz

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

// The seeded catalog is the security baseline — these invariants must hold or
// the whole guard model is undermined.
func TestDefaultRolesInvariants(t *testing.T) {
	roles := DefaultRoles()
	if len(roles) != 3 {
		t.Fatalf("expected exactly 3 seeded roles, got %d", len(roles))
	}

	byKey := map[string]models.Role{}
	for _, r := range roles {
		byKey[r.Key] = r
		if !r.System {
			t.Errorf("seeded role %q must be System:true", r.Key)
		}
		for _, p := range r.Permissions {
			if !IsValidRolePermission(p) {
				t.Errorf("seeded role %q has invalid permission %q", r.Key, p)
			}
		}
	}

	// The three expected keys at strictly increasing ranks.
	user, admin, su := byKey[models.RoleKeyUser], byKey[models.RoleKeyAdmin], byKey[models.RoleKeySuperuser]
	if !(user.Rank < admin.Rank && admin.Rank < su.Rank) {
		t.Errorf("ranks must be strictly increasing: user=%d admin=%d superuser=%d", user.Rank, admin.Rank, su.Rank)
	}

	// user: no permissions at all (no panel access).
	if len(user.Permissions) != 0 {
		t.Errorf("user role must have no permissions, got %v", user.Permissions)
	}

	// superuser: exactly the wildcard.
	if len(su.Permissions) != 1 || su.Permissions[0] != Wildcard {
		t.Errorf("superuser must hold exactly \"*\", got %v", su.Permissions)
	}

	// admin: has panel access, but NONE of the superuser-tier set.
	adminSet := map[string]bool{}
	for _, p := range admin.Permissions {
		adminSet[p] = true
	}
	if !adminSet[string(PermAdminAccess)] {
		t.Error("admin must hold admin.access")
	}
	for _, p := range []Permission{PermRolesWrite, PermUsersDelete, PermMigrationsRun} {
		if adminSet[string(p)] {
			t.Errorf("admin must NOT hold superuser-tier %q", p)
		}
	}
	if adminSet[Wildcard] {
		t.Error("admin must not hold the wildcard")
	}
}

func TestIsImmutableSystemRole(t *testing.T) {
	if !IsImmutableSystemRole(models.RoleKeyUser) {
		t.Error("user must be immutable")
	}
	if !IsImmutableSystemRole(models.RoleKeySuperuser) {
		t.Error("superuser must be immutable")
	}
	// admin stays API-tunable (guarded), so it is NOT immutable.
	if IsImmutableSystemRole(models.RoleKeyAdmin) {
		t.Error("admin must be mutable (tunable)")
	}
	if IsImmutableSystemRole("support") {
		t.Error("custom roles must be mutable")
	}
}
