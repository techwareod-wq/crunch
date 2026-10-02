package models

import "errors"

var (
	// ErrRoleConflictOnUser is returned by SetUserAccess when the caller's
	// expected role_updated_at doesn't match — another superuser changed this
	// user's access since it was read. Controllers map it to a 409.
	ErrRoleConflictOnUser = errors.New("user access changed since last read")
)
