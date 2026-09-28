package admin

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

func entry(key string) adminPermissionEntryDTO { return adminPermissionEntryDTO{Key: key} }

func TestValidateGrantEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []adminPermissionEntryDTO
		want    *apperrors.Error
	}{
		{"known non-tier ok", []adminPermissionEntryDTO{entry(string(authz.PermContentManage)), entry(string(authz.PermUsersRead))}, nil},
		{"unknown key 400", []adminPermissionEntryDTO{entry("bogus.key")}, apperrors.ErrUnknownPermissionKey},
		{"wildcard rejected 400", []adminPermissionEntryDTO{entry(authz.Wildcard)}, apperrors.ErrUnknownPermissionKey},
		{"superuser-tier forbidden 403", []adminPermissionEntryDTO{entry(string(authz.PermRolesWrite))}, apperrors.ErrRoleEscalation},
		{"users.delete forbidden 403", []adminPermissionEntryDTO{entry(string(authz.PermUsersDelete))}, apperrors.ErrRoleEscalation},
		{"admin.access forbidden in grants 403", []adminPermissionEntryDTO{entry(string(authz.PermAdminAccess))}, apperrors.ErrRoleEscalation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateGrantEntries(tc.entries); got != tc.want {
				t.Errorf("validateGrantEntries = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateRevokeEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []adminPermissionEntryDTO
		want    *apperrors.Error
	}{
		{"known non-tier ok", []adminPermissionEntryDTO{entry(string(authz.PermTrialsManage))}, nil},
		// admin.access is forbidden in revokes too (S1) — symmetric with grants:
		// revoking the universal baseline API-locks the target out of the whole
		// panel while they still count as an active superuser (an availability
		// foot-gun the role-count guard can't see).
		{"admin.access forbidden in revokes 403", []adminPermissionEntryDTO{entry(string(authz.PermAdminAccess))}, apperrors.ErrRoleEscalation},
		{"unknown key 400", []adminPermissionEntryDTO{entry("bogus.key")}, apperrors.ErrUnknownPermissionKey},
		// superuser-tier stays forbidden — a revoke of it could strip a
		// superuser's core powers past the count guard.
		{"superuser-tier forbidden 403", []adminPermissionEntryDTO{entry(string(authz.PermRolesWrite))}, apperrors.ErrRoleEscalation},
		{"wildcard rejected 400", []adminPermissionEntryDTO{entry(authz.Wildcard)}, apperrors.ErrUnknownPermissionKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateRevokeEntries(tc.entries); got != tc.want {
				t.Errorf("validateRevokeEntries = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPermsEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical", []string{"a", "b"}, []string{"a", "b"}, true},
		{"order-insensitive", []string{"a", "b"}, []string{"b", "a"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different content", []string{"a", "c"}, []string{"a", "b"}, false},
		{"both empty", []string{}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := permsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("permsEqual(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
