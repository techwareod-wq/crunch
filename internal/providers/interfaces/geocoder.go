package interfaces

import (
	"context"
	"errors"
)

// GeocodeResult is one resolved address.
type GeocodeResult struct {
	Lat     float64
	Lng     float64
	PlaceID string
	// Accuracy is the provider's precision label (Google location_type:
	// ROOFTOP, RANGE_INTERPOLATED, GEOMETRIC_CENTER, APPROXIMATE).
	Accuracy  string
	Formatted string
}

// AccuracyApproximate is Google's lowest-precision location_type. A pin this
// coarse blocks submit (D-061).
const AccuracyApproximate = "APPROXIMATE"

// ErrGeocodeNotFound means the provider answered but found nothing (not a
// transient failure; don't retry).
var ErrGeocodeNotFound = errors.New("address not found")

// Geocoder resolves a free-text address within a country (ISO 3166-1
// alpha-2). Used by the catalog (draft saves, D-061) and search (04).
type Geocoder interface {
	Geocode(ctx context.Context, address, country string) (GeocodeResult, error)
}
