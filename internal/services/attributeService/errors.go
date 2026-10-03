package attributeService

import (
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
)

var (
	// ErrNotFound: no node / field / industry with that key.
	ErrNotFound = errors.New("not found")
	// ErrKeyExists: a node, field or industry with that key already exists.
	ErrKeyExists = models.ErrDuplicateKey
	// ErrVersionConflict: the doc changed since the caller read it (CAS).
	ErrVersionConflict = models.ErrVersionConflict
)

// ValidationError is a 400: the write breaks a tree / rule invariant.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalid wraps err as a ValidationError.
func Invalid(err error) error { return &ValidationError{Msg: err.Error()} }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error { return Invalid(fmt.Errorf(format, a...)) }

// StaleSnapshotError: the cached rules snapshot is older than the version
// the caller asked for.
type StaleSnapshotError struct{ Have, Want int64 }

func (e *StaleSnapshotError) Error() string {
	return "rules snapshot is older than requested"
}
