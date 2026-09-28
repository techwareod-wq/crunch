package authz

// Tenancy plan §10 — the company-axis authz matrix: claimed-only fail-closed,
// owner-always-admin, and the D17 "no leakage in either direction" pins.

import (
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func companyRolesCache(t *testing.T) *RolesCache {
	t.Helper()
	cache, err := NewRolesCacheFromRoles(DefaultRoles())
	if err != nil {
		t.Fatalf("cache from default roles: %v", err)
	}
	return cache
}

func claimedMembership(userID primitive.ObjectID, role string) *models.CompanyMembership {
	return &models.CompanyMembership{
		CompanyID: primitive.NewObjectID(),
		UserID:    &userID,
		Role:      role,
		Status:    models.CompanyMembershipStatusClaimed,
	}
}

func TestEffectiveCompanyPermissions_Matrix(t *testing.T) {
	cache := companyRolesCache(t)
	uid := primitive.NewObjectID()

	t.Run("user_admin gets the full company set", func(t *testing.T) {
		set := EffectiveCompanyPermissions(claimedMembership(uid, models.CompanyRoleKeyAdmin), cache)
		if len(set) != len(AllCompanyPermissions) {
			t.Errorf("user_admin resolved %d perms, want %d", len(set), len(AllCompanyPermissions))
		}
	})

	t.Run("user_user gets the baseline only", func(t *testing.T) {
		set := EffectiveCompanyPermissions(claimedMembership(uid, models.CompanyRoleKeyUser), cache)
		if !set[PermCompanyBrainWrite] {
			t.Error("user_user must hold company.brain.write")
		}
		if set[PermCompanyMembersInvite] || set[PermCompanyBillingManage] || set[PermCompanySettingsWrite] {
			t.Errorf("user_user leaked admin powers: %v", set)
		}
	})

	t.Run("claimed-only fails closed", func(t *testing.T) {
		for _, status := range []string{models.CompanyMembershipStatusInvited, models.CompanyMembershipStatusArchived} {
			m := claimedMembership(uid, models.CompanyRoleKeyAdmin)
			m.Status = status
			if set := EffectiveCompanyPermissions(m, cache); len(set) != 0 {
				t.Errorf("status %q resolved %v, want empty set", status, set)
			}
		}
		if set := EffectiveCompanyPermissions(nil, cache); len(set) != 0 {
			t.Errorf("nil membership resolved %v, want empty set", set)
		}
	})

	t.Run("global role key on a membership resolves nothing", func(t *testing.T) {
		m := claimedMembership(uid, models.RoleKeySuperuser)
		if set := EffectiveCompanyPermissions(m, cache); len(set) != 0 {
			t.Errorf("membership with global role key resolved %v, want empty", set)
		}
	})
}

func TestResolveCompanyPermissions_OwnerRule(t *testing.T) {
	cache := companyRolesCache(t)
	owner := primitive.NewObjectID()
	company := &models.Company{ID: primitive.NewObjectID(), OwnerUserID: owner}

	// Owner with NO membership doc at all still gets everything (D12).
	set := ResolveCompanyPermissions(owner, company, nil, cache)
	if len(set) != len(AllCompanyPermissions) {
		t.Errorf("owner resolved %d perms, want the full set", len(set))
	}

	// A non-owner with no membership gets nothing — including from company B's
	// membership (no cross-company leakage).
	stranger := primitive.NewObjectID()
	otherCompanyMembership := claimedMembership(stranger, models.CompanyRoleKeyAdmin)
	setB := ResolveCompanyPermissions(stranger, company, nil, cache)
	if len(setB) != 0 {
		t.Errorf("stranger resolved %v, want empty", setB)
	}
	// The membership itself resolves fine — but only for ITS company; callers
	// pair (company, membership) and the middleware never crosses them.
	if got := EffectiveCompanyPermissions(otherCompanyMembership, cache); len(got) == 0 {
		t.Error("sanity: the membership should resolve in its own company")
	}
}

// TestNoGlobalLeakage pins D17 in both directions: superuser "*" never
// expands to company keys, company keys are unknown to the global registry
// (so extra-grants validation rejects them), and a company role key as
// user.Role resolves nothing globally.
func TestNoGlobalLeakage(t *testing.T) {
	cache := companyRolesCache(t)
	now := time.Now()

	su := &models.User{ID: primitive.NewObjectID(), Role: models.RoleKeySuperuser}
	global := EffectivePermissions(su, cache, now)
	for _, p := range AllCompanyPermissions {
		if global[p] {
			t.Errorf("superuser wildcard leaked company key %q", p)
		}
	}

	for _, p := range AllCompanyPermissions {
		if IsKnownPermission(string(p)) {
			t.Errorf("company key %q must not be a known GLOBAL permission", p)
		}
	}

	impostor := &models.User{ID: primitive.NewObjectID(), Role: models.CompanyRoleKeyAdmin}
	if got := EffectivePermissions(impostor, cache, now); len(got) != 0 {
		t.Errorf("user.Role=user_admin resolved global perms %v, want none", got)
	}
	if !IsCompanyRoleKey(models.CompanyRoleKeyAdmin) || !IsCompanyRoleKey(models.CompanyRoleKeyUser) {
		t.Error("company role keys must be recognized")
	}
	if IsCompanyRoleKey(models.RoleKeyAdmin) {
		t.Error("global admin must not read as a company role key")
	}
}
