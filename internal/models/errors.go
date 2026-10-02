package models

import "errors"

var (
	// ErrRoleConflictOnUser is returned by SetUserAccess when the caller's
	// expected role_updated_at doesn't match — another superuser changed this
	// user's access since it was read. Controllers map it to a 409.
	ErrRoleConflictOnUser = errors.New("user access changed since last read")
)

// WarehouseHub model errors.
var (
	// ErrNotFound: no document matched.
	ErrNotFound = errors.New("not found")
	// ErrDuplicateKey: a unique index refused the write (key, shortId, or a
	// second open revision).
	ErrDuplicateKey = errors.New("duplicate key")
	// ErrVersionConflict: a CAS write found the doc changed since it was read.
	ErrVersionConflict = errors.New("changed since last read")
)
