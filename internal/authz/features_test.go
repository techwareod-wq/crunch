package authz

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func TestHasFeature(t *testing.T) {
	cases := []struct {
		name string
		u    *models.User
		yes  []Feature
		no   []Feature
	}{
		{"nil", nil, nil, []Feature{FeatureAccess, FeatureSearch}},
		{"user, no grants", &models.User{Role: models.RoleUser}, nil, []Feature{FeatureAccess, FeatureSearch}},
		{"user, search", &models.User{Role: models.RoleUser, Features: []string{"search"}}, []Feature{FeatureAccess, FeatureSearch}, []Feature{FeatureAISearch, FeatureEnquiries}},
		{"user, unknown grant only", &models.User{Role: models.RoleUser, Features: []string{"bogus"}}, nil, []Feature{FeatureAccess, FeatureSearch}},
		{"admin", &models.User{Role: models.RoleAdmin}, []Feature{FeatureAccess, FeatureSearch, FeatureAISearch, FeatureListings, FeatureEnquiries}, nil},
		{"superuser", &models.User{Role: models.RoleSuperuser}, []Feature{FeatureAccess, FeatureEnquiries}, nil},
	}
	for _, c := range cases {
		for _, f := range c.yes {
			if !HasFeature(c.u, f) {
				t.Errorf("%s: want %q", c.name, f)
			}
		}
		for _, f := range c.no {
			if HasFeature(c.u, f) {
				t.Errorf("%s: must not have %q", c.name, f)
			}
		}
	}
}

func TestEffectiveFeatures(t *testing.T) {
	if got := EffectiveFeatures(&models.User{Role: models.RoleUser, Features: []string{"enquiries", "search", "bogus"}}); len(got) != 2 || got[0] != "search" || got[1] != "enquiries" {
		t.Fatalf("user: %v", got)
	}
	if got := EffectiveFeatures(&models.User{Role: models.RoleAdmin}); len(got) != len(Features) {
		t.Fatalf("staff: %v", got)
	}
	if got := EffectiveFeatures(&models.User{Role: models.RoleUser}); got == nil || len(got) != 0 {
		t.Fatalf("no access: %#v", got)
	}
}
