package collectors

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/utils"
)

// Audit market resolution. DataForSEO Labs ranked-keyword data is
// PER-LOCATION: querying the US database for an India-targeted site reports
// "0 ranked keywords" no matter how well it ranks on google.co.in. The crawl
// stage resolves one location per run (tenant WebEntity setting → ccTLD →
// homepage <html lang> region → US default) and stamps it on the run doc;
// the authority + serp collectors read it through RunLocationCode. All
// code/name lookups ride the shared boot-time locations.Catalog — the only
// hand-rolled pieces are the signals the catalog can't know: which ccTLDs
// carry no market intent, and the uk→gb alias.

const (
	// DefaultLocationCode is the fallback market (United States) — the
	// pre-resolution behavior, and what unstamped legacy runs keep so their
	// rechecks compare like-for-like.
	DefaultLocationCode = 2840
	auditLanguageName   = "English"
)

// RunLocationCode is what the market-sensitive collectors query with: the
// run's stamped location, or the US default for unstamped (legacy) runs.
func RunLocationCode(run *models.AuditRun) int {
	if run != nil && run.LocationCode > 0 {
		return run.LocationCode
	}
	return DefaultLocationCode
}

// InferLocationFromDomain maps a country-code TLD to its market ("example.in"
// → India) via the shared catalog. Generic-use TLDs return 0 — no signal.
func InferLocationFromDomain(catalog *locations.Catalog, domain string) int {
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(domain, ".")), ".")
	if len(labels) < 2 {
		return 0
	}
	tld := labels[len(labels)-1]
	// ccTLDs that overwhelmingly host generic/tech sites carry no market
	// signal despite technically being country codes.
	switch tld {
	case "ai", "io", "co", "me", "tv", "cc", "ly", "sh", "app", "dev":
		return 0
	}
	if tld == "uk" {
		tld = "gb" // the one ccTLD that isn't its ISO alpha-2 code
	}
	return catalog.CodeForISO(tld)
}

// InferLocationFromLang maps an <html lang> region subtag to its market
// ("en-IN" → India) via the shared catalog. A bare language ("en") carries
// no region — 0.
func InferLocationFromLang(catalog *locations.Catalog, lang string) int {
	parts := strings.Split(strings.TrimSpace(lang), "-")
	if len(parts) < 2 {
		return 0
	}
	region := parts[len(parts)-1]
	if len(region) != 2 {
		return 0
	}
	return catalog.CodeForISO(region)
}

// htmlLangRe pulls the lang attribute off the <html> element of a raw fetch.
var htmlLangRe = regexp.MustCompile(`(?is)<html[^>]*\slang=["']([^"']+)["']`)

// ProbeHomepageLang GETs the homepage (no JS — the attribute is static
// markup) and returns its <html lang> value. Best-effort: any failure
// returns "" and the caller falls through to the next signal.
func ProbeHomepageLang(ctx context.Context, domain string) string {
	client := &http.Client{Timeout: siteProbeTimeout}
	body, err := utils.FetchURLBody(ctx, client, "https://"+domain, 256<<10)
	if err != nil {
		return ""
	}
	if m := htmlLangRe.FindSubmatch(body); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}
