package authz

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func TestHas(t *testing.T) {
	cases := []struct {
		name string
		u    *models.User
		yes  []Permission
		no   []Permission
	}{
		{"nil", nil, nil, []Permission{PermAdmin, PermEditor}},
		{"plain user", &models.User{Role: models.RoleUser, Permissions: []string{"editor"}}, nil, []Permission{PermAdmin, PermEditor}},
		{"admin, no ticks", &models.User{Role: models.RoleAdmin}, []Permission{PermAdmin}, []Permission{PermEditor, PermApprover, PermSuperuser}},
		{"admin editor", &models.User{Role: models.RoleAdmin, Permissions: []string{"editor"}}, []Permission{PermAdmin, PermEditor}, []Permission{PermApprover, PermSuperuser}},
		{"admin both", &models.User{Role: models.RoleAdmin, Permissions: []string{"editor", "approver"}}, []Permission{PermEditor, PermApprover}, []Permission{PermSuperuser}},
		{"admin can't self-hold superuser", &models.User{Role: models.RoleAdmin, Permissions: []string{"superuser"}}, nil, []Permission{PermSuperuser}},
		{"superuser", &models.User{Role: models.RoleSuperuser}, []Permission{PermAdmin, PermEditor, PermApprover, PermSuperuser}, nil},
		{"unknown role", &models.User{Role: "approver", Permissions: []string{"approver"}}, nil, []Permission{PermAdmin, PermApprover}},
	}
	for _, c := range cases {
		for _, p := range c.yes {
			if !Has(c.u, p) {
				t.Errorf("%s: want %s", c.name, p)
			}
		}
		for _, p := range c.no {
			if Has(c.u, p) {
				t.Errorf("%s: must not have %s", c.name, p)
			}
		}
	}
	if got := Effective(&models.User{Role: models.RoleAdmin, Permissions: []string{"approver"}}); len(got) != 2 || got[1] != "approver" {
		t.Fatalf("Effective = %v", got)
	}
}
