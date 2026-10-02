package dto

// Overview is GET /v1/admin/analytics/search/overview.
type Overview struct {
	From   string     `json:"from"`
	To     string     `json:"to"`
	Totals Totals     `json:"totals"`
	Daily  []DayPoint `json:"daily"`
}

// Totals sums the range. Percentages are 0–100; the AI latencies are the
// daily percentiles averaged by AI-search volume (approximate).
type Totals struct {
	Searches            int64   `json:"searches"`
	AISearches          int64   `json:"aiSearches"`
	ZeroResult          int64   `json:"zeroResult"`
	FallbackUsed        int64   `json:"fallbackUsed"`
	Expanded            int64   `json:"expanded"`
	AIParseFailures     int64   `json:"aiParseFailures"`
	Enquiries           int64   `json:"enquiries"`
	EnquiriesFromSearch int64   `json:"enquiriesFromSearch"`
	ZeroResultPct       float64 `json:"zeroResultPct"`
	FallbackPct         float64 `json:"fallbackPct"`
	AIParseFailurePct   float64 `json:"aiParseFailurePct"`
	AILatencyP50        float64 `json:"aiLatencyP50"`
	AILatencyP95        float64 `json:"aiLatencyP95"`
}

// DayPoint is one day of the series.
type DayPoint struct {
	Date                string  `json:"date"`
	Searches            int64   `json:"searches"`
	AISearches          int64   `json:"aiSearches"`
	ZeroResult          int64   `json:"zeroResult"`
	FallbackUsed        int64   `json:"fallbackUsed"`
	Expanded            int64   `json:"expanded"`
	AIParseFailures     int64   `json:"aiParseFailures"`
	Enquiries           int64   `json:"enquiries"`
	EnquiriesFromSearch int64   `json:"enquiriesFromSearch"`
	AILatencyP50        float64 `json:"aiLatencyP50"`
	AILatencyP95        float64 `json:"aiLatencyP95"`
}

// TopQueries is GET /v1/admin/analytics/search/top.
type TopQueries struct {
	From  string     `json:"from"`
	To    string     `json:"to"`
	Items []TopQuery `json:"items"`
}

// TopQuery is one normalized query. Enquiries counts enquiries that came
// from it (searches still inside the 90-day raw window).
type TopQuery struct {
	Q           string  `json:"q"`
	Searches    int64   `json:"searches"`
	ZeroResults int64   `json:"zeroResults"`
	ZeroShare   float64 `json:"zeroShare"`
	Enquiries   int64   `json:"enquiries"`
}

// ZeroQueries is GET /v1/admin/analytics/search/zero-results: the
// missing-supply signal.
type ZeroQueries struct {
	From  string      `json:"from"`
	To    string      `json:"to"`
	Items []ZeroQuery `json:"items"`
}

// ZeroQuery is one query + place that found nothing.
type ZeroQuery struct {
	Q     string `json:"q"`
	Place string `json:"place,omitempty"`
	N     int64  `json:"n"`
}

// Conversion is GET /v1/admin/analytics/search/conversion (D-105): an
// enquiry carrying a searchId converts that search.
type Conversion struct {
	From                string `json:"from"`
	To                  string `json:"to"`
	Searches            int64  `json:"searches"`
	SearchesWithResults int64  `json:"searchesWithResults"`
	Enquiries           int64  `json:"enquiries"`
	EnquiriesFromSearch int64  `json:"enquiriesFromSearch"`
	// Rate is enquiriesFromSearch / searches, 0–100.
	Rate      float64             `json:"rate"`
	ByQuery   []QueryConversion   `json:"byQuery"`
	ByListing []ListingConversion `json:"byListing"`
}

// QueryConversion is one query's searches → enquiries.
type QueryConversion struct {
	Q         string  `json:"q"`
	Searches  int64   `json:"searches"`
	Enquiries int64   `json:"enquiries"`
	Rate      float64 `json:"rate"`
}

// ListingConversion is one listing's enquiries.
type ListingConversion struct {
	WarehouseID string `json:"warehouseId"`
	ShortID     string `json:"shortId"`
	Name        string `json:"name"`
	Enquiries   int64  `json:"enquiries"`
	FromSearch  int64  `json:"fromSearch"`
}
