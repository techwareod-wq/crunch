package models

import (
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	// ErrOverridesConflict is returned by SetUserOverrides when expectedUpdatedAt
	// doesn't match the stored overrides_updated_at — another admin edited the
	// overrides since they were read. Controllers map it to a 409.
	ErrOverridesConflict = errors.New("overrides changed since last read")

	// ErrRoleConflictOnUser is returned by SetUserRole / SetUserGrants when the
	// caller's expected role_updated_at doesn't match the stored value — another
	// admin changed this user's role or grants since they were read. Controllers
	// map it to a 409. Mirrors ErrOverridesConflict.
	ErrRoleConflictOnUser = errors.New("user role/grants changed since last read")

	// ErrRoleConflict is returned by UpsertRole when the caller's expectedUpdatedAt
	// doesn't match the stored updated_at — another admin edited the role since it
	// was read. Controllers map it to a 409. Mirrors ErrOverridesConflict.
	ErrRoleConflict = errors.New("role changed since last read")

	// ErrRoleKeyExists is returned by UpsertRole when a create (expected == nil)
	// collides with an existing role key.
	ErrRoleKeyExists = errors.New("role key already exists")
)

// parseOID converts a hex string into an ObjectID, wrapping a failed parse in
// the package's standard "invalid <kind> ID: %w" error message.
func parseOID(kind string, hex string) (primitive.ObjectID, error) {
	oid, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("invalid "+kind+" ID: %w", err)
	}
	return oid, nil
}
