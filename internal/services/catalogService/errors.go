package catalogService

import (
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
)

// ErrNotFound: no warehouse / revision / media with that id (or slug).
var ErrNotFound = models.ErrNotFound

// ValidationError is a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// StateError is a 409 with a machine code: the action isn't allowed in the
// current state (in_review lock, already open, not live…).
type StateError struct{ Code, Msg string }

func (e *StateError) Error() string { return e.Msg }

// Conflict formats a StateError.
func Conflict(code, format string, a ...any) error {
	return &StateError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// SubmitBlockedError is a 400 listing what stops a submit.
type SubmitBlockedError struct{ Problems []string }

func (e *SubmitBlockedError) Error() string {
	return "submit blocked: " + strings.Join(e.Problems, "; ")
}

// StateError codes.
const (
	CodeInReview     = "in_review"
	CodeBadState     = "bad_state"
	CodeStale        = "version_conflict"
	CodeOpenExists   = "open_revision_exists"
	CodeStaleBase    = "stale_base"
	CodeNotUploaded  = "not_uploaded"
	CodeGeocodingOff = "geocoding_unavailable"
)
