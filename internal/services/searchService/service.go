// Package searchService is WarehouseHub structured search (spec 04): the
// filter → Mongo query, radius rings, facets and sorting, the map points,
// the public filter catalog, and resolving a typed location (cache → Google
// → pincode table). It also serves the catalog's 410 "nearby" list
// (domain.SearchEngine).
package searchService

import (
	"context"

	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Viewer is who is searching (optional auth, P-5), for analytics.
type Viewer struct {
	UserID string
	Staff  bool
	// SessionID is the visitor's anonymous session (D-105).
	SessionID string
	// Quiet skips the search log: AI search (05) logs its own, richer event.
	Quiet bool
	// Admin is the staff search: every attribute is queryable (staff-only
	// and non-filterable too), archived listings on request, and cards
	// carry the id, status and projection. Never logged.
	Admin bool
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
	// cached per catalogVersion; etag is set for it (D-077). admin: the
	// staff scope (any attribute, archived on request, never cached) with
	// each point's warehouse id and status.
	Map(ctx context.Context, f *domain.SearchFilters, country string, admin bool) (dto.MapResponse, string, error)
	// Catalog is the filter catalog (chips, ranges, grouped attributes,
	// industries, radius steps); etag follows rulesVersion. admin lists
	// every attribute, not just public + filterable ones.
	Catalog(country string, admin bool) (dto.PublicCatalog, string)
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
