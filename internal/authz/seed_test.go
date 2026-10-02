package authz

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

// The seeded catalog is the security baseline — these invariants must hold or
// the whole guard model is undermined.
func TestDefaultRolesInvariants(t *testing.T) {
	roles := DefaultRoles()
	if len(roles) != 4 {
		t.Fatalf("expected exactly 4 seeded roles, got %d", len(roles))
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
	if _, ok := byKey[models.RoleKeyLegacyAdmin]; ok {
		t.Error("the legacy admin role must not be seeded (D-013)")
	}

	user, editor, approver, su := byKey[models.RoleKeyUser], byKey[models.RoleKeyEditor], byKey[models.RoleKeyApprover], byKey[models.RoleKeySuperuser]
	if !(user.Rank < editor.Rank && editor.Rank < approver.Rank && approver.Rank < su.Rank) {
		t.Errorf("ranks must be strictly increasing: user=%d editor=%d approver=%d superuser=%d", user.Rank, editor.Rank, approver.Rank, su.Rank)
	}

	// user: no permissions at all (no panel access).
	if len(user.Permissions) != 0 {
		t.Errorf("user role must have no permissions, got %v", user.Permissions)
	}

	// superuser: exactly the wildcard.
	if len(su.Permissions) != 1 || su.Permissions[0] != Wildcard {
		t.Errorf("superuser must hold exactly \"*\", got %v", su.Permissions)
	}

	// editor and approver: panel access, but NONE of the superuser-tier set.
	for _, r := range []models.Role{editor, approver} {
		set := toSet(r.Permissions)
		if !set[string(PermAdminAccess)] {
			t.Errorf("%s must hold admin.access", r.Key)
		}
		if set[Wildcard] {
			t.Errorf("%s must not hold the wildcard", r.Key)
		}
		for p := range set {
			if IsSuperuserTier(p) {
				t.Errorf("%s must NOT hold superuser-tier %q", r.Key, p)
			}
		}
	}

	// approver ⊇ editor.
	approverSet := toSet(approver.Permissions)
	for _, p := range editor.Permissions {
		if !approverSet[p] {
			t.Errorf("approver must hold every editor permission; missing %q", p)
		}
	}
}

// TestPermissionMatrix pins who can do what (D-012/D-013 and the platform spec
// test list): an editor cannot approve, archive, read audit, read analytics or
// manage attributes/industries; only the superuser can invite staff.
func TestPermissionMatrix(t *testing.T) {
	cache, err := NewRolesCacheFromRoles(DefaultRoles())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		perm                              Permission
		user, editor, approver, superuser bool
	}{
		{PermAdminAccess, false, true, true, true},
		{PermWarehousesRead, false, true, true, true},
		{PermWarehousesEdit, false, true, true, true},
		{PermAnswersWrite, false, true, true, true},
		{PermEnquiriesRead, false, true, true, true},
		{PermEnquiriesWrite, false, true, true, true},
		{PermWarehousesApprove, false, false, true, true},
		{PermWarehousesArchive, false, false, true, true},
		{PermAttributesManage, false, false, true, true},
		{PermIndustriesManage, false, false, true, true},
		{PermAuditRead, false, false, true, true},
		{PermAnalyticsRead, false, false, true, true},
		{PermStaffInvite, false, false, false, true},
		{PermRolesWrite, false, false, false, true},
	}
	for _, c := range cases {
		for role, want := range map[string]bool{
			models.RoleKeyUser:      c.user,
			models.RoleKeyEditor:    c.editor,
			models.RoleKeyApprover:  c.approver,
			models.RoleKeySuperuser: c.superuser,
		} {
			if got := Has(&models.User{Role: role}, c.perm, cache, authzNow); got != want {
				t.Errorf("%s has %s = %t, want %t", role, c.perm, got, want)
			}
		}
	}
}

func TestIsImmutableSystemRole(t *testing.T) {
	if !IsImmutableSystemRole(models.RoleKeyUser) {
		t.Error("user must be immutable")
	}
	if !IsImmutableSystemRole(models.RoleKeySuperuser) {
		t.Error("superuser must be immutable")
	}
	// editor/approver stay API-tunable (guarded), so they are NOT immutable.
	for _, k := range []string{models.RoleKeyEditor, models.RoleKeyApprover, "support"} {
		if IsImmutableSystemRole(k) {
			t.Errorf("%s must be mutable (tunable)", k)
		}
	}
}

func toSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
