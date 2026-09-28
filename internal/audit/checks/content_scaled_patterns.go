package checks

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- content.scaled_patterns (quality uplift 2.5 — the March-2024
// scaled-content-abuse fingerprint: publishing burst + template uniformity on
// a young domain. Directly relevant to blogs WE generate, so the framing is
// deliberately corrective, not accusatory) ---

type contentScaledPatterns struct{}

func (contentScaledPatterns) ID() core.CheckID          { return "content.scaled_patterns" }
func (contentScaledPatterns) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentScaledPatterns) Kind() CheckKind           { return KindDeterministic }
func (contentScaledPatterns) Requires() []core.Kind {
	return []core.Kind{artifacts.KindCrawl, artifacts.KindHTMLDeep}
}

// scaledPatternsMinPages: below this many content pages, template statistics
// are noise, not signal.
const scaledPatternsMinPages = 5

var (
	parentheticalHookRe = regexp.MustCompile(`\(.*\)\s*$`)
	numericListicleRe   = regexp.MustCompile(`(?i)(^\d+\s)|(\btop\s+\d+\b)`)
	skeletonTokenRe     = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

func (contentScaledPatterns) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()
	deep, _ := in.Bundle.HTMLDeep()

	// Content pages: crawled, indexable, with substance and a title.
	type contentPage struct{ url, title string }
	var pages []contentPage
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect || p.WordCount < 300 || strings.TrimSpace(p.Title) == "" {
			continue
		}
		pages = append(pages, contentPage{url: p.URL, title: strings.TrimSpace(p.Title)})
	}
	if len(pages) < scaledPatternsMinPages {
		// Too few content pages to exhibit (or rule out) a scaled footprint —
		// not assessed.
		return core.CheckResult{Score: nil}, nil
	}

	// Template signals over titles.
	hooks, listicles := 0, 0
	skeletons := map[string]int{}
	for _, p := range pages {
		if parentheticalHookRe.MatchString(p.title) {
			hooks++
		}
		if numericListicleRe.MatchString(p.title) {
			listicles++
		}
		skeletons[titleSkeleton(p.title)]++
	}
	topSkeleton := 0
	for _, n := range skeletons {
		if n > topSkeleton {
			topSkeleton = n
		}
	}
	hookShare := float64(hooks) / float64(len(pages))
	listicleShare := float64(listicles) / float64(len(pages))
	skeletonShare := float64(topSkeleton) / float64(len(pages))

	// Burst signal from sitemap lastmods matched to content pages.
	urlSet := make(map[string]bool, len(pages))
	for _, p := range pages {
		urlSet[normalizeCheckURL(p.url)] = true
	}
	burst, burstWindow := detectPublishingBurst(deep.Sitemap.Entries, urlSet)

	// Build-stamp guard: when EVERY sitemap entry (content and utility pages
	// alike) carries one identical lastmod, the generator wrote deploy time,
	// not publish dates — that's noise, not a publishing burst (the
	// technical.sitemap check already nudges about the uniform lastmod).
	if burst && sitemapLastmodsUniform(deep.Sitemap.Entries) {
		burst, burstWindow = false, ""
	}

	earned := 100.0
	var signals []string
	if burst {
		earned -= 40
		signals = append(signals, fmt.Sprintf("a publishing burst (%s)", burstWindow))
	}
	// The skeleton reduces to word-count + punctuation — at the 5-page
	// minimum, four plain six-word titles collide by coincidence. Require a
	// base where the statistic means something.
	if skeletonShare > 0.6 && len(pages) >= 10 {
		earned -= 30
		signals = append(signals, fmt.Sprintf("%.0f%% of titles share one structural template", skeletonShare*100))
	}
	if hookShare > 0.7 {
		earned -= 15
		signals = append(signals, fmt.Sprintf("%.0f%% of titles end in a parenthetical hook", hookShare*100))
	}
	if listicleShare > 0.7 {
		earned -= 15
		signals = append(signals, fmt.Sprintf("%.0f%% of titles are numeric listicles", listicleShare*100))
	}

	var findings []core.Finding
	if len(signals) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.scaled_patterns",
			Severity:       core.SeverityMedium,
			Title:          "Publishing pattern matches the scaled-content-abuse fingerprint",
			Detail:         fmt.Sprintf("The site shows %s. This pattern-matches what Google's March-2024 scaled-content policy targets — the issue is the FOOTPRINT, not any single article.", strings.Join(signals, "; ")),
			Recommendation: "The fix is cadence + format variety + first-hand data, not deletion: spread publishing over time, vary title formats, and inject original data/experience into the strongest pieces.",
			Falsifiability: "A re-crawl shows publish dates spread beyond a single burst window and no single title template above 60% of content pages.",
		})
	}

	// Template twins (findings-only): title pairs differing by ≤2 tokens are
	// the same article with a token swapped — the city-swap programmatic
	// pattern near-duplicate detection misses because the BODIES differ enough
	// to pass similarity thresholds while the PAGES compete for adjacent
	// queries.
	type titledPage struct{ url, title string }
	var twinInputs []titledPage
	for _, p := range pages {
		twinInputs = append(twinInputs, titledPage{url: p.url, title: p.title})
	}
	var twinDescriptions []string
	twinPages := map[string]bool{}
	for i := 0; i < len(twinInputs); i++ {
		for j := i + 1; j < len(twinInputs); j++ {
			if titlesAreTwins(twinInputs[i].title, twinInputs[j].title) {
				if len(twinDescriptions) < 3 {
					twinDescriptions = append(twinDescriptions, fmt.Sprintf("%q vs %q", twinInputs[i].title, twinInputs[j].title))
				}
				twinPages[twinInputs[i].url] = true
				twinPages[twinInputs[j].url] = true
			}
		}
	}
	if len(twinPages) > 0 {
		var urls []string
		for u := range twinPages {
			urls = append(urls, u)
		}
		sort.Strings(urls)
		findings = append(findings, core.Finding{
			CheckID:        "content.scaled_patterns",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) are template twins — the same article with tokens swapped", len(urls)),
			Detail:         fmt.Sprintf("Title pairs like %s differ by only a token or two — the location/keyword-swap footprint. Thin variants competing for adjacent queries split authority and read as programmatic generation.", strings.Join(twinDescriptions, "; ")),
			Pages:          capPages(urls),
			Recommendation: "Merge each twin set into one authoritative guide with per-variant sections (e.g. one national guide with city sections), and 301 the variants into it.",
			Falsifiability: "A re-crawl finds no two content-page titles differing by two or fewer tokens.",
		})
	}

	return core.CheckResult{
		Score:    fixedScore(earned),
		Findings: findings,
		Evidence: map[string]any{
			"hookShare":     hookShare,
			"listicleShare": listicleShare,
			"skeletonShare": skeletonShare,
			"burst":         burst,
		},
	}, nil
}

// titleSkeleton strips letters/digits down to a structural template: "7 Best
// CRMs (2026 Guide)" → "w w w (w w)".
func titleSkeleton(title string) string {
	s := skeletonTokenRe.ReplaceAllString(title, "w")
	return strings.Join(strings.Fields(s), " ")
}

// yearVersionTokenRe marks tokens that legitimately differentiate editions:
// 4-digit years and explicit version tokens ("2025", "v3"). Bare small
// numbers stay twin-eligible — a leading listicle count ("7 Best…" vs
// "9 Best…") is the template footprint, not an edition.
var yearVersionTokenRe = regexp.MustCompile(`^(?:20\d{2}|v\d{1,3})$`)

// titlesAreTwins reports whether two titles are the same article with a token
// swapped: both ≥6 tokens, not identical, and each side carries ≤2 tokens the
// other lacks (multiset difference — repeated tokens count). Pairs whose
// differing tokens are ALL years/version numbers are exempt — "Best CRM 2025"
// vs "Best CRM 2026" are yearly editions, and recommending merge+301 for them
// is destructive advice.
func titlesAreTwins(a, b string) bool {
	ta := skeletonTokenRe.FindAllString(strings.ToLower(a), -1)
	tb := skeletonTokenRe.FindAllString(strings.ToLower(b), -1)
	if len(ta) < 6 || len(tb) < 6 {
		return false
	}
	counts := map[string]int{}
	for _, t := range ta {
		counts[t]++
	}
	var onlyB []string
	for _, t := range tb {
		if counts[t] > 0 {
			counts[t]--
		} else {
			onlyB = append(onlyB, t)
		}
	}
	var onlyA []string
	for t, n := range counts {
		for i := 0; i < n; i++ {
			onlyA = append(onlyA, t)
		}
	}
	if len(onlyA) == 0 && len(onlyB) == 0 {
		return false // identical titles are the duplicate-title check's turf
	}
	if len(onlyA) > 2 || len(onlyB) > 2 {
		return false
	}
	editionOnly := true
	for _, t := range append(append([]string{}, onlyA...), onlyB...) {
		if !yearVersionTokenRe.MatchString(t) {
			editionOnly = false
			break
		}
	}
	return !editionOnly
}

// sitemapLastmodsUniform reports whether every dated sitemap entry shares one
// identical lastmod — the deploy-stamp signature.
func sitemapLastmodsUniform(entries []artifacts.SitemapProbeEntry) bool {
	first, dated := "", 0
	for _, e := range entries {
		if e.LastMod == "" {
			continue
		}
		dated++
		if first == "" {
			first = e.LastMod
		} else if e.LastMod != first {
			return false
		}
	}
	return dated >= 5
}

// detectPublishingBurst reports whether ≥10 content pages are dated within
// one 7-day window AND that window covers ≥80% of all dated content pages.
// Dates come from sitemap lastmods matched to crawled content pages; when the
// sitemap and crawl share no URLs (different normalization regimes), all
// dated entries stand in.
func detectPublishingBurst(entries []artifacts.SitemapProbeEntry, contentURLs map[string]bool) (bool, string) {
	var dates []time.Time
	matched := 0
	var allDates []time.Time
	for _, e := range entries {
		if e.LastMod == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", e.LastMod)
		if err != nil {
			continue
		}
		allDates = append(allDates, t)
		if contentURLs[normalizeCheckURL(e.URL)] {
			matched++
			dates = append(dates, t)
		}
	}
	if matched == 0 {
		dates = allDates
	}
	if len(dates) < 10 {
		return false, ""
	}
	sort.Slice(dates, func(a, b int) bool { return dates[a].Before(dates[b]) })

	window := 7 * 24 * time.Hour
	best, bestStart := 0, time.Time{}
	lo := 0
	for hi := range dates {
		for dates[hi].Sub(dates[lo]) > window {
			lo++
		}
		if n := hi - lo + 1; n > best {
			best = n
			bestStart = dates[lo]
		}
	}
	if best >= 10 && float64(best) >= 0.8*float64(len(dates)) {
		return true, fmt.Sprintf("%d of %d dated pages within the week of %s", best, len(dates), bestStart.Format("2006-01-02"))
	}
	return false, ""
}
