package authz

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func newTestRolesCache(t *testing.T, roles []models.Role) *RolesCache {
	t.Helper()
	c := &RolesCache{}
	if err := c.install(roles); err != nil {
		t.Fatalf("install: %v", err)
	}
	return c
}

func TestRolesCacheByKey(t *testing.T) {
	c := newTestRolesCache(t, DefaultRoles())

	if r, ok := c.ByKey(models.RoleKeyApprover); !ok || r.Rank != RankApprover {
		t.Errorf("ByKey(admin) = %v, %v; want admin rank %d", r, ok, RankApprover)
	}
	if _, ok := c.ByKey("ghost"); ok {
		t.Error("unknown key must not resolve")
	}

	// Nil receiver tolerance (cache not wired in tests).
	var nilCache *RolesCache
	if _, ok := nilCache.ByKey(models.RoleKeyApprover); ok {
		t.Error("nil cache must resolve nothing")
	}
	if got := nilCache.Keys(); got != nil {
		t.Errorf("nil cache Keys() = %v, want nil", got)
	}
}

func TestRolesCacheEmptyCatalogOK(t *testing.T) {
	// Deploys before the seed run: zero docs must not fail, just resolve nothing.
	c := newTestRolesCache(t, nil)
	if _, ok := c.ByKey(models.RoleKeySuperuser); ok {
		t.Error("empty cache must resolve nothing")
	}
	if got := c.Keys(); len(got) != 0 {
		t.Errorf("empty cache Keys() = %v, want none", got)
	}
}

func TestRolesCacheDuplicateKeyRejectedKeepsSnapshot(t *testing.T) {
	c := newTestRolesCache(t, DefaultRoles())

	dup := append(DefaultRoles(), models.Role{Key: models.RoleKeyApprover, Rank: 99, Permissions: []string{Wildcard}})
	if err := c.install(dup); err == nil {
		t.Fatal("duplicate role key must reject the reload")
	}
	// Previous snapshot keeps serving — admin still resolves at its old rank.
	if r, ok := c.ByKey(models.RoleKeyApprover); !ok || r.Rank != RankApprover {
		t.Errorf("after rejected reload, admin rank = %v (ok=%v); want %d from old snapshot", r, ok, RankApprover)
	}
}

func TestRolesCacheUnknownPermissionRejectedKeepsSnapshot(t *testing.T) {
	c := newTestRolesCache(t, DefaultRoles())

	bad := append(DefaultRoles(), models.Role{Key: "broken", Rank: 5, Permissions: []string{"not.a.real.perm"}})
	if err := c.install(bad); err == nil {
		t.Fatal("role with an unknown permission must reject the reload")
	}
	// Old snapshot intact; the broken key never became resolvable.
	if _, ok := c.ByKey("broken"); ok {
		t.Error("rejected snapshot must not leak the broken role")
	}
	if _, ok := c.ByKey(models.RoleKeySuperuser); !ok {
		t.Error("old snapshot must remain intact after rejected reload")
	}
}

func TestRolesCacheEmptyKeyRejected(t *testing.T) {
	c := &RolesCache{}
	if err := c.install([]models.Role{{Key: "", Rank: 0}}); err == nil {
		t.Error("a role with an empty key must be rejected")
	}
}
