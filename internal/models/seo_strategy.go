package models

// SEO strategies bifurcate the Site Intelligence Engine by how ambitious the
// keyword hunt should be for the site's authority. The strategy id selects a
// DataForSEO filter set and opportunity-score knobs from
// values.siteIntelligence.strategies (see config.SIEStrategyValues); the ids
// here are the catalog the patch endpoint validates against and the frontend
// renders copy for. New strategies are added by extending this catalog plus a
// matching values entry.
const (
	// SEOStrategyBalancedGrowth targets the standard growth mix: real search
	// demand with a domain-rating-relative difficulty band. The default for
	// sites with some authority, and the pre-strategy behavior.
	SEOStrategyBalancedGrowth = "balanced_growth"
	// SEOStrategyEarlyFootholds targets small, low-competition searches
	// (modest volume, fixed low difficulty ceiling) a weak or new domain can
	// realistically win first.
	SEOStrategyEarlyFootholds = "early_footholds"
)

// SEOStrategyEarlyFootholdsMaxDR is the domain-rating cutoff for the default
// strategy: below it a site defaults to early_footholds, at or above it to
// balanced_growth. An unknown rating (0 — DataForSEO had no authority data,
// typical for brand-new domains) therefore also defaults to early_footholds.
const SEOStrategyEarlyFootholdsMaxDR = 20

var validSEOStrategies = map[string]bool{
	SEOStrategyBalancedGrowth: true,
	SEOStrategyEarlyFootholds: true,
}

func IsValidSEOStrategy(id string) bool {
	return validSEOStrategies[id]
}

// DefaultSEOStrategyForDR derives the recommended strategy from a domain
// rating. This is the onboarding default; the user may override it there or
// from settings (persisted as WebEntity.SEOStrategy).
func DefaultSEOStrategyForDR(dr int) string {
	if dr < SEOStrategyEarlyFootholdsMaxDR {
		return SEOStrategyEarlyFootholds
	}
	return SEOStrategyBalancedGrowth
}
