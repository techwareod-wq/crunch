// Package authz decides who may do what on the /v1/admin surface.
//
// The model is deliberately small and lives entirely in code — no roles
// collection, no cache:
//
//   - every user has one role: user (no panel), admin, or superuser;
//   - an admin additionally holds any of the assignable permissions editor and
//     approver (one person can hold both);
//   - superuser holds everything and is the only one who changes roles and
//     permissions. Superusers are minted only by cmd/superuser.
//
// Routes gate on a Permission: PermAdmin (any panel user), PermEditor,
// PermApprover or PermSuperuser.
package authz

import (
	"slices"

	"github.com/atharva-ng/crunch/internal/models"
)

// Permission is what a route requires.
type Permission string

const (
	// PermAdmin is panel access: role admin or superuser.
	PermAdmin Permission = "admin"
	// PermEditor: edit listing drafts, submit/withdraw, answer needs-info,
	// work the enquiry inbox.
	PermEditor Permission = "editor"
	// PermApprover: approve/reject/bulk-approve, archive/restore, edit
	// attribute and industry rules, read the change log and analytics.
	PermApprover Permission = "approver"
	// PermSuperuser: user access management, user deletion, cron force-runs,
	// data seeds.
	PermSuperuser Permission = "superuser"
)

// Assignable is what a superuser can tick on an admin.
var Assignable = []Permission{PermEditor, PermApprover}

// IsAssignable reports whether p may be stored on a user.
func IsAssignable(p string) bool {
	return slices.Contains(Assignable, Permission(p))
}

// Has reports whether u holds p. Fails closed on a nil user or unknown role.
func Has(u *models.User, p Permission) bool {
	if u == nil {
		return false
	}
	switch u.Role {
	case models.RoleSuperuser:
		return true
	case models.RoleAdmin:
		switch p {
		case PermAdmin:
			return true
		case PermEditor, PermApprover:
			return slices.Contains(u.Permissions, string(p))
		}
	}
	return false
}

// Effective lists everything u holds, for whoami (the admin UI gates menu
// items on it).
func Effective(u *models.User) []string {
	out := []string{}
	for _, p := range []Permission{PermAdmin, PermEditor, PermApprover, PermSuperuser} {
		if Has(u, p) {
			out = append(out, string(p))
		}
	}
	return out
}
