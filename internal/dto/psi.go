package dto

// --- Request/response types for the Google PageSpeed Insights v5 API
// (audit Performance category — CrUX field data is the scoring input,
// Lighthouse lab data rides along as finding detail; decision 6). ---

// PagespeedRequest shapes the runPagespeed GET (the provider renders it as
// query params).
type PagespeedRequest struct {
	URL      string
	Strategy string // "mobile" | "desktop"
}

// PagespeedResponse carries only the fields the audit consumes.
type PagespeedResponse struct {
	// LoadingExperience is page-level CrUX field data (absent for
	// low-traffic pages).
	LoadingExperience *PSILoadingExperience `json:"loadingExperience"`
	// OriginLoadingExperience is origin-level CrUX field data — the audit's
	// scoring input.
	OriginLoadingExperience *PSILoadingExperience `json:"originLoadingExperience"`
	LighthouseResult        *PSILighthouseResult  `json:"lighthouseResult"`
}

type PSILoadingExperience struct {
	Metrics map[string]PSIMetric `json:"metrics"`
	// OverallCategory is "FAST" | "AVERAGE" | "SLOW" (empty when CrUX has no
	// data for the origin/page).
	OverallCategory string `json:"overall_category"`
}

// PSIMetric is one CrUX metric's 75th-percentile value. For
// CUMULATIVE_LAYOUT_SHIFT_SCORE the percentile is the CLS × 100.
type PSIMetric struct {
	Percentile float64 `json:"percentile"`
	Category   string  `json:"category"`
}

// CrUX metric keys in the metrics map.
const (
	PSIMetricLCP = "LARGEST_CONTENTFUL_PAINT_MS"
	PSIMetricINP = "INTERACTION_TO_NEXT_PAINT"
	PSIMetricCLS = "CUMULATIVE_LAYOUT_SHIFT_SCORE"
)

type PSILighthouseResult struct {
	Categories *PSICategories          `json:"categories"`
	Audits     map[string]PSIAuditItem `json:"audits"`
}

type PSICategories struct {
	Performance *PSICategoryScore `json:"performance"`
}

type PSICategoryScore struct {
	// Score is 0-1; the artifact stores it ×100.
	Score float64 `json:"score"`
}

type PSIAuditItem struct {
	ID      string           `json:"id"`
	Title   string           `json:"title"`
	Details *PSIAuditDetails `json:"details"`
}

type PSIAuditDetails struct {
	Type             string  `json:"type"` // "opportunity" marks savings audits
	OverallSavingsMs float64 `json:"overallSavingsMs"`
}
