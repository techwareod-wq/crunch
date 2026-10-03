package enquiryService

import (
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
)

// ErrNotFound: no enquiry (or listing) with that id.
var ErrNotFound = models.ErrNotFound

// ErrListingGone: the enquiry names an archived listing (410).
var ErrListingGone = errors.New("listing has been removed")

// ValidationError is a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// ErrStale is a 409: the enquiry changed since the caller read it.
var ErrStale = errors.New("someone else changed this enquiry — reload")
