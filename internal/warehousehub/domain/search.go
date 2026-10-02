package domain

import (
	"context"
	"time"
)

// Structured search (spec 04). AI search (05) produces the same
// SearchFilters and gets the same SearchResponse.

// Sorts (D-076).
const (
	SortRelevance = "relevance"
	SortDistance  = "distance"
	SortPriceAsc  = "price_asc"
	SortPriceDesc = "price_desc"
	SortAreaDesc  = "area_desc"
)

// Geocode sources (D-078) and degraded flags.
const (
	GeoSourcePoint   = "point"
	GeoSourceCache   = "cache"
	GeoSourceGoogle  = "google"
	GeoSourcePincode = "pincode"

	DegradedGeocode  = "geocode_unavailable"
	DegradedSemantic = "semantic_unavailable"
)

// Fallback reasons and sources (05, D-084 / D-088).
const (
	FallbackFewResults    = "few_results"
	FallbackNothingMapped = "nothing_mapped"
	FallbackLLMFailed     = "llm_failed"

	FallbackSemantic = "semantic"
	FallbackKeyword  = "keyword"
)

// SearchFilters is the search input (POST /v1/public/search body).
type SearchFilters struct {
	Location   *SearchLocation `json:"location,omitempty"`
	RadiusKm   int             `json:"radiusKm,omitempty"`
	AutoExpand *bool           `json:"autoExpand,omitempty"`
	// AreaSqm filters on total area (D-131). AreaInput echoes what the user
	// typed (for the UI).
	AreaSqm   *MinMax    `json:"areaSqm,omitempty"`
	AreaInput *AreaInput `json:"areaInput,omitempty"`
	// Price is per sq m per month in Currency minor units (D-058).
	Price      *PriceFilter `json:"price,omitempty"`
	PriceInput any          `json:"priceInput,omitempty"`
	// Industries must ALL pass (D-037).
	Industries        []string `json:"industries,omitempty"`
	IncludeUnverified bool     `json:"includeUnverified,omitempty"`
	// Chips are ANDed (D-072): "node", "node.field" (bool) or
	// "node.field:option".
	Chips []string `json:"chips,omitempty"`
	// Ranges filter numeric fields by full path ("node.field"), canonical
	// units. On a range field, min means "max ≥ min" and max "min ≤ max".
	Ranges map[string]MinMax `json:"ranges,omitempty"`
	// Text is the raw NL query (05: logging / fallback).
	Text  string `json:"text,omitempty"`
	Sort  string `json:"sort,omitempty"`
	Page  int    `json:"page,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// SearchLocation is one of postal code, place text or a point.
type SearchLocation struct {
	PostalCode string  `json:"postalCode,omitempty"`
	Place      string  `json:"place,omitempty"`
	Point      *LatLng `json:"point,omitempty"`
	Country    string  `json:"country,omitempty"`
}

// LatLng is a WGS84 point.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// MinMax is an optional numeric range.
type MinMax struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// Empty reports whether neither bound is set.
func (m MinMax) Empty() bool { return m.Min == nil && m.Max == nil }

// AreaInput is the area as typed (UI echo).
type AreaInput struct {
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Intent string  `json:"intent"` // min | max | approx
}

// PriceFilter bounds the normalized price.
type PriceFilter struct {
	Currency       string   `json:"currency,omitempty"`
	PerSqmMonthMin *float64 `json:"perSqmMonthMin,omitempty"`
	PerSqmMonthMax *float64 `json:"perSqmMonthMax,omitempty"`
}

// Active reports whether the price filter bounds anything.
func (p *PriceFilter) Active() bool {
	return p != nil && (p.PerSqmMonthMin != nil || p.PerSqmMonthMax != nil)
}

// SearchResponse is the search output.
type SearchResponse struct {
	SearchID string          `json:"searchId"`
	Applied  SearchApplied   `json:"applied"`
	Radius   *SearchRadius   `json:"radius"`
	Results  []SearchCard    `json:"results"`
	Page     int             `json:"page"`
	Limit    int             `json:"limit"`
	Total    int64           `json:"total"`
	Facets   SearchFacets    `json:"facets"`
	Fallback *SearchFallback `json:"fallback"`
	Degraded []string        `json:"degraded"`
}

// SearchApplied echoes what was actually searched.
type SearchApplied struct {
	Filters       SearchFilters `json:"filters"`
	ResolvedPoint *LatLng       `json:"resolvedPoint,omitempty"`
	GeocodeSource string        `json:"geocodeSource,omitempty"`
	Dropped       []string      `json:"dropped"`
}

// SearchRadius reports the ring used (D-071).
type SearchRadius struct {
	RequestedKm int    `json:"requestedKm"`
	UsedKm      int    `json:"usedKm"`
	Expanded    bool   `json:"expanded"`
	Exhausted   bool   `json:"exhausted"`
	Message     string `json:"message,omitempty"`
	// CountryLink: nothing within the last ring; the UI links the
	// country-wide view (D-071).
	CountryLink bool `json:"countryLink"`
}

// SearchCard is one result.
type SearchCard struct {
	ShortID      string      `json:"shortId"`
	Slug         string      `json:"slug"`
	Name         string      `json:"name"`
	City         string      `json:"city,omitempty"`
	Locality     string      `json:"locality,omitempty"`
	DistKm       *float64    `json:"distKm,omitempty"`
	TotalSqm     float64     `json:"totalSqm"`
	TotalDisplay string      `json:"totalDisplay,omitempty"`
	Rate         *PublicRate `json:"rate,omitempty"`
	// Industries the listing fits (F or P; Partial shows as fit, D-034).
	Industries []string `json:"industries"`
	// Unverified: matched only through the include-unverified path (D-035).
	Unverified bool   `json:"unverified"`
	CoverURL   string `json:"coverUrl,omitempty"`
}

// SearchFacets are counts / bounds within the current results.
type SearchFacets struct {
	Chips      map[string]int64         `json:"chips"`
	Industries map[string]IndustryCount `json:"industries"`
	Ranges     map[string]Bounds        `json:"ranges"`
	Price      *Bounds                  `json:"price"`
	Area       *Bounds                  `json:"area"`
}

// IndustryCount splits an industry's matches.
type IndustryCount struct {
	Fit        int64 `json:"fit"`
	Unverified int64 `json:"unverified"`
}

// Bounds is a min–max seen in the results.
type Bounds struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// SearchFallback carries 05's similar matches.
type SearchFallback struct {
	Used   bool   `json:"used"`
	Reason string `json:"reason"`
	// Source: semantic (vector) or keyword ($text).
	Source  string       `json:"source"`
	Results []SearchCard `json:"results"`
}

// SearchAI describes the natural-language parse (05).
type SearchAI struct {
	// Parsed: the model returned usable filters; false = basic search on
	// the regex pre-parse (D-088).
	Parsed    bool     `json:"parsed"`
	Model     string   `json:"model"`
	LatencyMs int64    `json:"latencyMs"`
	Notes     []string `json:"notes"`
}

// SearchEvent is one logged search (07).
type SearchEvent struct {
	SearchID string
	At       time.Time
	// Source: "structured" or "ai".
	Source   string
	Filters  SearchFilters
	Total    int64
	UsedKm   int
	Degraded []string
	// UserID is empty for anonymous searches; Staff marks admin users so
	// analytics can exclude them.
	UserID string
	Staff  bool
	// AI is set for natural-language searches (05); Fallback names the
	// similar-matches reason when they were added.
	AI       *SearchAI
	Fallback string
}

// SearchLogger records searches (implemented by analytics, 07). Called in
// a goroutine with a detached ctx; it must never block or fail a search.
type SearchLogger interface {
	Log(ctx context.Context, e SearchEvent)
}
