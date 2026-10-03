package authz

import (
	"slices"

	"github.com/atharva-ng/crunch/internal/models"
)

// Feature is a visitor-facing capability a superuser grants per user from the
// admin panel (models.User.Features). Nobody pays in crunch; features are
// plain on/off access grants.
type Feature string

const (
	// FeatureAccess is the "any access at all" gate: the user holds at least
	// one feature. Routes without a specific feature (profile) use it.
	FeatureAccess Feature = ""
	// FeatureSearch: structured search, map, filter catalog, geo resolve.
	FeatureSearch Feature = "search"
	// FeatureAISearch: natural-language search.
	FeatureAISearch Feature = "ai_search"
	// FeatureListings: public listing pages, slugs and sitemap.
	FeatureListings Feature = "listings"
	// FeatureEnquiries: the enquiry form.
	FeatureEnquiries Feature = "enquiries"
)

// Features is every grantable feature, in canonical order.
var Features = []Feature{FeatureSearch, FeatureAISearch, FeatureListings, FeatureEnquiries}

// IsFeature reports whether f is a grantable feature.
func IsFeature(f string) bool {
	return slices.Contains(Features, Feature(f))
}

// HasFeature reports whether u may use f. Staff (admin panel access) hold
// every feature so they can use the site they run. FeatureAccess needs any
// granted feature. Fails closed.
func HasFeature(u *models.User, f Feature) bool {
	if u == nil {
		return false
	}
	if Has(u, PermAdmin) {
		return true
	}
	if f == FeatureAccess {
		return slices.ContainsFunc(u.Features, IsFeature)
	}
	return slices.Contains(u.Features, string(f))
}

// EffectiveFeatures lists the features u may use, in canonical order (never
// nil).
func EffectiveFeatures(u *models.User) []string {
	out := []string{}
	for _, f := range Features {
		if HasFeature(u, f) {
			out = append(out, string(f))
		}
	}
	return out
}
