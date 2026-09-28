package authz

import "testing"

func TestIsKnownPermission(t *testing.T) {
	if !IsKnownPermission("users.read") {
		t.Error("users.read must be known")
	}
	if !IsKnownPermission(string(PermRolesWrite)) {
		t.Error("roles.write must be known")
	}
	if IsKnownPermission("content.typo") {
		t.Error("unknown key must not validate")
	}
	// The wildcard is NOT a known permission — it is only valid in a role's
	// permission list, checked via IsValidRolePermission / Wildcard.
	if IsKnownPermission(Wildcard) {
		t.Error(`"*" must not be a known permission`)
	}
}

func TestIsSuperuserTier(t *testing.T) {
	for _, p := range []string{string(PermRolesWrite), string(PermUsersDelete), string(PermMigrationsRun), string(PermCronManage), Wildcard} {
		if !IsSuperuserTier(p) {
			t.Errorf("%q must be superuser-tier", p)
		}
	}
	for _, p := range []string{string(PermAdminAccess), string(PermRolesRead), string(PermUsersRead), string(PermRolesRead)} {
		if IsSuperuserTier(p) {
			t.Errorf("%q must NOT be superuser-tier", p)
		}
	}
}

func TestIsValidRolePermission(t *testing.T) {
	if !IsValidRolePermission(Wildcard) {
		t.Error(`"*" must be valid in a role permission list`)
	}
	if !IsValidRolePermission(string(PermRolesRead)) {
		t.Error("a known permission must be valid in a role permission list")
	}
	if IsValidRolePermission("nope") {
		t.Error("unknown key must be invalid in a role permission list")
	}
}

// AllPermissions must stay in sync with knownPermissions — a constant added to
// one but not the other would silently mis-validate.
func TestAllPermissionsCoverKnownSet(t *testing.T) {
	if len(AllPermissions) != len(knownPermissions) {
		t.Fatalf("AllPermissions has %d entries, knownPermissions has %d", len(AllPermissions), len(knownPermissions))
	}
	for _, p := range AllPermissions {
		if !knownPermissions[string(p)] {
			t.Errorf("%q in AllPermissions missing from knownPermissions", p)
		}
	}
}
