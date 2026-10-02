// Package searchService is WarehouseHub structured search (spec 04): the
// filter → Mongo query, radius rings, facets and sorting, the map points,
// the public filter catalog, and resolving a typed location (cache → Google
// → pincode table). It also serves the catalog's 410 "nearby" list
// (domain.SearchEngine).
package searchService

import (
	"context"
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
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

// Viewer is who is searching (optional auth, P-5), for analytics.
type Viewer struct {
	UserID string
	Staff  bool
	// Quiet skips the search log: AI search (05) logs its own, richer event.
	Quiet bool
}

// FallbackQuery bounds the similar / keyword matches.
type FallbackQuery struct {
	Country string
	// Near drops matches beyond the last radius ring (nil = no location).
	Near *domain.LatLng
	// Exclude lists shortIds already shown.
	Exclude []string
	Limit   int
	// VectorIndex / NumCandidates tune $vectorSearch (Similar only).
	VectorIndex   string
	NumCandidates int
}

// SearchService runs structured search.
type SearchService interface {
	domain.SearchEngine

	// Search runs one search. Location failures degrade, never error
	// (D-078); only malformed filters are a ValidationError.
	Search(ctx context.Context, f domain.SearchFilters, v Viewer) (domain.SearchResponse, error)
	// Map returns every matching pin. f nil = the unfiltered country view,
	// cached per catalogVersion; etag is set for it (D-077).
	Map(ctx context.Context, f *domain.SearchFilters, country string) (dto.MapResponse, string, error)
	// Catalog is the public filter catalog (chips, ranges, industries,
	// radius steps); etag follows rulesVersion.
	Catalog(country string) (dto.PublicCatalog, string)
	// Resolve geocodes a typed postal code / place (map centring).
	Resolve(ctx context.Context, q, country string) (dto.GeoResolved, error)

	// Similar returns listings nearest to vec by meaning (Atlas Vector
	// Search, D-084); Keyword the best $text matches for text (AI parser
	// down, D-088). Both keep to live listings in q.Country, within the last
	// radius ring of q.Near, and skip q.Exclude.
	Similar(ctx context.Context, vec []float32, q FallbackQuery) ([]domain.SearchCard, error)
	Keyword(ctx context.Context, text string, q FallbackQuery) ([]domain.SearchCard, error)

	// SetLogger wires analytics (07); nil = no logging.
	SetLogger(l domain.SearchLogger)
}
