package searchService

import (
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
)

// ErrNotFound: a location that resolves nowhere.
var ErrNotFound = models.ErrNotFound

// ErrGeoUnavailable: every geocoding source failed (resolve endpoint only;
// search degrades instead, D-078).
var ErrGeoUnavailable = errors.New("geocoding unavailable")

// ValidationError is a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// ErrNoGeocoder: no geocoding provider configured.
var ErrNoGeocoder = errors.New("no geocoder configured")

// ErrGeocoderCircuitOpen: the geocoder breaker is open.
var ErrGeocoderCircuitOpen = errors.New("geocoder circuit open")
