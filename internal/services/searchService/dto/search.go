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
	// AdminPoints (admin map only) holds each point's warehouse id and
	// status, in Points order.
	AdminPoints []AdminMapPoint `json:"adminPoints,omitempty"`
}

// AdminMapPoint links an admin map point to its warehouse.
type AdminMapPoint struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// PublicCatalog is GET /v1/public/catalog.
type PublicCatalog struct {
	RulesVersion    int64         `json:"rulesVersion"`
	Country         string        `json:"country"`
	Currency        string        `json:"currency"`
	DefaultRadiusKm int           `json:"defaultRadiusKm"`
	RadiusSteps     []int         `json:"radiusSteps"`
	ChipRows        []ChipRow     `json:"chipRows"`
	Ranges          []RangeFilter `json:"ranges"`
	// Groups is the same set by attribute, in tree order, for the "add a
	// filter" picker.
	Groups     []FilterGroup  `json:"groups"`
	Industries []IndustryItem `json:"industries"`
}

// FilterGroup is one attribute node and its filterable fields.
type FilterGroup struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	ParentName string `json:"parentName,omitempty"`
	// Ancestors are the parent node keys, nearest first (root excluded): a
	// filter inside this node implies them.
	Ancestors []string `json:"ancestors"`
	// Selectable: the node itself is a chip ("has cold storage").
	Selectable bool          `json:"selectable"`
	Public     bool          `json:"public"`
	Fields     []FilterField `json:"fields"`
}

// FilterField is one filterable field: a bool chip (Key), pick / multi
// option chips (Options, "<path>:<option>") or a numeric range (Key, Unit;
// range-typed fields match on overlap).
type FilterField struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Unit    string `json:"unit,omitempty"`
	Public  bool   `json:"public"`
	Options []Chip `json:"options,omitempty"`
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
