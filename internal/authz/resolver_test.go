package authz

import (
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

var authzNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// seedCache is the three system roles plus a custom "support"-style role at the
// gap rank, used to exercise the resolver against realistic data.
func seedCache(t *testing.T) *RolesCache {
	t.Helper()
	roles := DefaultRoles()
	roles = append(roles, models.Role{
		Key:         "support",
		Rank:        10,
		Permissions: []string{string(PermAdminAccess), string(PermUsersRead)},
		System:      false,
	})
	c, err := NewRolesCacheFromRoles(roles)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	return c
}

func TestEffectivePermissions(t *testing.T) {
	cache := seedCache(t)

	// superuser: "*" expands to the whole catalog.
	su := EffectivePermissions(&models.User{Role: models.RoleKeySuperuser}, cache, authzNow)
	if len(su) != len(AllPermissions) {
		t.Errorf("superuser perms = %d, want all %d", len(su), len(AllPermissions))
	}
	if !su[PermMigrationsRun] || !su[PermRolesWrite] || !su[PermUsersDelete] {
		t.Error("superuser must hold every superuser-tier permission")
	}

	// admin: the fixed bundle, and crucially NONE of the superuser-tier set.
	admin := EffectivePermissions(&models.User{Role: models.RoleKeyAdmin}, cache, authzNow)
	if !admin[PermAdminAccess] || !admin[PermRolesRead] || !admin[PermUsersRead] {
		t.Error("admin must hold panel access + manage perms")
	}
	for _, p := range []Permission{PermRolesWrite, PermUsersDelete, PermMigrationsRun} {
		if admin[p] {
			t.Errorf("admin must NOT hold superuser-tier %q", p)
		}
	}

	// user: empty — no admin access.
	if got := EffectivePermissions(&models.User{Role: models.RoleKeyUser}, cache, authzNow); len(got) != 0 {
		t.Errorf("user perms = %v, want none", got)
	}

	// unknown role: fail closed.
	if got := EffectivePermissions(&models.User{Role: "ghost"}, cache, authzNow); len(got) != 0 {
		t.Errorf("unknown role perms = %v, want none (fail closed)", got)
	}

	// nil user / nil cache: fail closed.
	if got := EffectivePermissions(nil, cache, authzNow); len(got) != 0 {
		t.Errorf("nil user perms = %v, want none", got)
	}
	if Has(&models.User{Role: models.RoleKeyAdmin}, PermRolesRead, nil, authzNow) {
		t.Error("nil cache must resolve no role permissions (fail closed)")
	}
}

func TestEffectivePermissionsGrantsAndRevokes(t *testing.T) {
	cache := seedCache(t)
	past := authzNow.Add(-time.Hour)
	future := authzNow.Add(time.Hour)
	entry := func(key string, exp *time.Time) models.OverrideEntry {
		return models.OverrideEntry{Key: key, ExpiresAt: exp, By: "su@x.com", At: past}
	}

	// A grant adds a permission the role lacks (support gains roles.read).
	u := &models.User{Role: "support", ExtraGrants: []models.OverrideEntry{entry(string(PermRolesRead), nil)}}
	if !Has(u, PermRolesRead, cache, authzNow) {
		t.Error("grant must add roles.read")
	}

	// A revoke removes a permission the role has (support loses users.read).
	u = &models.User{Role: "support", ExtraRevokes: []models.OverrideEntry{entry(string(PermUsersRead), nil)}}
	if Has(u, PermUsersRead, cache, authzNow) {
		t.Error("revoke must remove users.read")
	}

	// Revoke beats grant on the same key.
	u = &models.User{Role: "support",
		ExtraGrants:  []models.OverrideEntry{entry(string(PermRolesRead), nil)},
		ExtraRevokes: []models.OverrideEntry{entry(string(PermRolesRead), nil)}}
	if Has(u, PermRolesRead, cache, authzNow) {
		t.Error("revoke must win over grant on the same key")
	}

	// Expired grant is ignored.
	u = &models.User{Role: "support", ExtraGrants: []models.OverrideEntry{entry(string(PermRolesRead), &past)}}
	if Has(u, PermRolesRead, cache, authzNow) {
		t.Error("expired grant must be ignored")
	}
	// Live grant (future expiry) applies.
	u = &models.User{Role: "support", ExtraGrants: []models.OverrideEntry{entry(string(PermRolesRead), &future)}}
	if !Has(u, PermRolesRead, cache, authzNow) {
		t.Error("unexpired grant must apply")
	}
	// Expired revoke is ignored — the role permission stays.
	u = &models.User{Role: "support", ExtraRevokes: []models.OverrideEntry{entry(string(PermUsersRead), &past)}}
	if !Has(u, PermUsersRead, cache, authzNow) {
		t.Error("expired revoke must be ignored (permission stays)")
	}

	// Unknown grant key is dropped, not surfaced.
	u = &models.User{Role: "support", ExtraGrants: []models.OverrideEntry{entry("bogus.key", nil)}}
	if EffectivePermissions(u, cache, authzNow)[Permission("bogus.key")] {
		t.Error("unknown grant key must be dropped")
	}
}

func TestRank(t *testing.T) {
	cache := seedCache(t)
	cases := []struct {
		role string
		want int
	}{
		{models.RoleKeySuperuser, 30},
		{models.RoleKeyAdmin, 20},
		{"support", 10},
		{models.RoleKeyUser, 0},
		{"ghost", 0}, // unknown → 0, fail closed
		{"", 0},
	}
	for _, tc := range cases {
		if got := Rank(&models.User{Role: tc.role}, cache); got != tc.want {
			t.Errorf("Rank(%q) = %d, want %d", tc.role, got, tc.want)
		}
	}
	if got := Rank(nil, cache); got != 0 {
		t.Errorf("Rank(nil) = %d, want 0", got)
	}
	if got := Rank(&models.User{Role: models.RoleKeyAdmin}, nil); got != 0 {
		t.Errorf("Rank with nil cache = %d, want 0 (fail closed)", got)
	}
}

func TestEffectivePermissionKeysSortedAndExpanded(t *testing.T) {
	cache := seedCache(t)
	keys := EffectivePermissionKeys(&models.User{Role: models.RoleKeySuperuser}, cache, authzNow)
	if len(keys) != len(AllPermissions) {
		t.Fatalf("superuser keys = %d, want %d (\"*\" expanded)", len(keys), len(AllPermissions))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Errorf("keys not sorted: %v", keys)
			break
		}
	}
}

func TestExpandKeysAndSubset(t *testing.T) {
	// "*" expands to the full set.
	if got := ExpandKeys([]string{Wildcard}); len(got) != len(AllPermissions) {
		t.Errorf(`ExpandKeys(["*"]) = %d perms, want all %d`, len(got), len(AllPermissions))
	}
	// Unknown keys are dropped.
	got := ExpandKeys([]string{string(PermRolesRead), "nope"})
	if len(got) != 1 || !got[PermRolesRead] {
		t.Errorf("ExpandKeys dropped-unknown = %v, want {roles.read}", got)
	}

	super := ExpandKeys([]string{Wildcard})
	sub := ExpandKeys([]string{string(PermRolesRead), string(PermUsersRead)})
	if !IsSubset(sub, super) {
		t.Error("a role's perms must be a subset of superuser's full set")
	}
	if IsSubset(super, sub) {
		t.Error("the full set must NOT be a subset of a two-perm set")
	}
	// Empty is a subset of anything.
	if !IsSubset(map[Permission]bool{}, sub) {
		t.Error("empty set must be a subset")
	}
}
