package models

import "testing"

// roleSpecEqual gates whether SeedRoles treats a role as unchanged (a wrong
// comparison would either re-write every seed run or skip a real change).
func TestRoleSpecEqual(t *testing.T) {
	base := &Role{Key: "admin", Rank: 20, Permissions: []string{"admin.access", "users.read"}, Description: "d", System: true}

	cases := []struct {
		name string
		mut  func(r *Role)
		want bool
	}{
		{"identical", func(r *Role) {}, true},
		{"rank differs", func(r *Role) { r.Rank = 21 }, false},
		{"description differs", func(r *Role) { r.Description = "e" }, false},
		{"system differs", func(r *Role) { r.System = false }, false},
		{"permissions differ", func(r *Role) { r.Permissions = []string{"admin.access"} }, false},
		{"permission order differs (treated as change)", func(r *Role) { r.Permissions = []string{"users.read", "admin.access"} }, false},
		{"key ignored", func(r *Role) { r.Key = "other" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := *base
			b.Permissions = append([]string{}, base.Permissions...)
			tc.mut(&b)
			if got := roleSpecEqual(base, &b); got != tc.want {
				t.Errorf("roleSpecEqual = %v, want %v", got, tc.want)
			}
		})
	}
}
