// Package dto holds the search service's public response shapes. The
// search request/response themselves are domain.SearchFilters /
// domain.SearchResponse (shared with AI search, 05).
package dto

// MapResponse is GET /v1/public/search/map: compact pins the browser
// clusters (D-077). Each point is [shortId, lat, lng, priceBand]; priceBand
// is 0 = no price / on request, 1–3 = cheapest → dearest third.
type MapResponse struct {
	Points [][4]any  `json:"points"`
	BBox   []float64 `json:"bbox"` // [minLng, minLat, maxLng, maxLat]; empty without points
	Total  int       `json:"total"`
}

// PublicCatalog is GET /v1/public/catalog.
type PublicCatalog struct {
	RulesVersion    int64          `json:"rulesVersion"`
	Country         string         `json:"country"`
	Currency        string         `json:"currency"`
	DefaultRadiusKm int            `json:"defaultRadiusKm"`
	RadiusSteps     []int          `json:"radiusSteps"`
	ChipRows        []ChipRow      `json:"chipRows"`
	Ranges          []RangeFilter  `json:"ranges"`
	Industries      []IndustryItem `json:"industries"`
}

// ChipRow is one row of chips (a node's or field's filterRow).
type ChipRow struct {
	Row   string `json:"row"`
	Chips []Chip `json:"chips"`
}

// Chip is one toggleable filter: a node, a bool field or a pick option.
type Chip struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// RangeFilter is a numeric filter (number, area, ratio, range fields) in
// canonical units.
type RangeFilter struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Unit  string `json:"unit,omitempty"`
	Row   string `json:"row,omitempty"`
}

// IndustryItem is one selectable industry.
type IndustryItem struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// GeoResolved is GET /v1/public/geo/resolve.
type GeoResolved struct {
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Label  string  `json:"label"`
	Source string  `json:"source"`
}
