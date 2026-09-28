package dto

// GSCSite is one property the service account can see (sites.list).
type GSCSite struct {
	SiteURL         string `json:"siteUrl"`         // "sc-domain:foo.com" or "https://www.foo.com/"
	PermissionLevel string `json:"permissionLevel"` // "siteOwner" | "siteFullUser" | "siteRestrictedUser" | "siteUnverifiedUser"
}

// GSCQueryRequest parameterizes one searchanalytics.query call. Dates are
// "2006-01-02" strings in the property's reporting zone (Pacific Time — the
// analytics clock owns all date math). RowLimit caps the TOTAL rows returned
// across pagination (0 = everything); the provider pages internally with the
// API's 25k per-request ceiling.
type GSCQueryRequest struct {
	StartDate  string
	EndDate    string
	Dimensions []string // e.g. ["date"], ["date","page"], ["date","page","query"]
	RowLimit   int
}

// GSCRow is one searchanalytics.query result row. Keys align positionally
// with the request's Dimensions. Rows arrive sorted by clicks descending, so
// a RowLimit cap keeps the top-N by clicks.
type GSCRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}
