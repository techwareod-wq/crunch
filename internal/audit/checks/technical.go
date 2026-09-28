package checks

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- technical.broken_links ---

type technicalBrokenLinks struct{}

func (technicalBrokenLinks) ID() core.CheckID          { return "technical.broken_links" }
func (technicalBrokenLinks) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalBrokenLinks) Kind() CheckKind           { return KindDeterministic }
func (technicalBrokenLinks) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (technicalBrokenLinks) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()
	total := crawl.InternalLinksTotal
	broken := len(crawl.BrokenLinks)

	// A failed links fetch degraded to an empty list — zero broken links was
	// never verified, so score nothing rather than a free 100. (Legacy
	// artifacts lack the flag and also renormalize here; honest over lucky.)
	if !crawl.LinksAssessed && broken == 0 {
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	if broken > 0 {
		sev := core.SeverityMedium
		if broken >= 10 {
			sev = core.SeverityHigh
		}
		var pages []string
		for _, l := range crawl.BrokenLinks {
			pages = append(pages, l.ToURL)
		}
		findings = append(findings, core.Finding{
			CheckID:        "technical.broken_links",
			Severity:       sev,
			Title:          fmt.Sprintf("%d broken internal link(s)", broken),
			Detail:         fmt.Sprintf("The crawl found %d internal links pointing at pages that fail to load (of %d internal links checked).", broken, total),
			Pages:          capPages(pages),
			Recommendation: "Fix or remove each broken link: update the href to the page's current URL, or 301-redirect the dead target.",
			Falsifiability: "Re-crawl the site: the broken internal link count reaches zero and every listed target returns HTTP 200.",
		})
	}

	// Score by the share of internal links that resolve. A summary that
	// omitted the totals (total 0) must still charge the broken links it
	// found — 100/100 next to a broken-links finding is a contradiction.
	if total < broken {
		total = broken
	}
	return core.CheckResult{Score: ratioScore(total-broken, total), Findings: findings}, nil
}

// --- technical.redirects_status ---

type technicalRedirectsStatus struct{}

func (technicalRedirectsStatus) ID() core.CheckID          { return "technical.redirects_status" }
func (technicalRedirectsStatus) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalRedirectsStatus) Kind() CheckKind           { return KindDeterministic }
func (technicalRedirectsStatus) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (technicalRedirectsStatus) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var errorPages, redirectPages []string
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode == 0 {
			continue // never fetched — not assessed, neither pass nor fail
		}
		assessed++
		switch {
		case p.StatusCode >= 400:
			errorPages = append(errorPages, p.URL)
		case p.IsRedirect || (p.StatusCode >= 300 && p.StatusCode < 400):
			redirectPages = append(redirectPages, p.URL)
		}
	}

	var findings []core.Finding
	if len(errorPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.redirects_status",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%d crawled page(s) return 4xx/5xx", len(errorPages)),
			Detail:         "Pages the crawler reached through internal links respond with an error status — dead ends for both users and crawlers.",
			Pages:          capPages(errorPages),
			Recommendation: "Restore, redirect (301), or remove internal links to each erroring URL.",
			Falsifiability: "Re-crawl: every listed URL returns 200 or is no longer linked internally.",
		})
	}
	if len(redirectPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.redirects_status",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d internally-linked redirect(s)", len(redirectPages)),
			Detail:         "Internal links resolve through redirects, wasting crawl budget and latency on every hop.",
			Pages:          capPages(redirectPages),
			Recommendation: "Point internal links directly at each redirect's final destination.",
			Falsifiability: "Re-crawl: no internally-linked URL responds 3xx.",
		})
	}

	if assessed == 0 {
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	// A redirect is a latency nit (Low), not a dead end — it charges half of
	// what an error page does.
	bad := float64(len(errorPages)) + 0.5*float64(len(redirectPages))
	return core.CheckResult{Score: fixedScore((float64(assessed) - bad) / float64(assessed) * 100), Findings: findings}, nil
}

// --- technical.canonicalization ---

type technicalCanonicalization struct{}

func (technicalCanonicalization) ID() core.CheckID          { return "technical.canonicalization" }
func (technicalCanonicalization) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalCanonicalization) Kind() CheckKind           { return KindDeterministic }
func (technicalCanonicalization) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (technicalCanonicalization) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var missing, brokenChain, mismatchPairs, mismatchPages []string
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect {
			continue
		}
		// A page whose meta block never parsed (no title, no description, no
		// canonical) is "not assessed", never a missing-canonical failure.
		if p.Canonical == "" && p.Title == "" && p.MetaDescription == "" {
			continue
		}
		assessed++
		if p.Canonical == "" {
			missing = append(missing, p.URL)
			continue
		}
		if p.CanonicalChain || p.CanonicalToRedirect || p.CanonicalToBroken || p.RecursiveCanonical {
			brokenChain = append(brokenChain, p.URL)
			continue
		}
		// Quality uplift 1.4: a self-canonical that is normalized-equal but
		// string-unequal (trailing slash / www / scheme variant) is a
		// consistency nit worth half weight. Canonicals to genuinely
		// different pages stay untouched — that's legit variant consolidation.
		if p.Canonical != p.URL && normalizeCheckURL(p.Canonical) == normalizeCheckURL(p.URL) {
			mismatchPages = append(mismatchPages, p.URL)
			mismatchPairs = append(mismatchPairs, fmt.Sprintf("%s → canonical %s", p.URL, p.Canonical))
		}
	}

	var findings []core.Finding
	if len(missing) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.canonicalization",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) without a canonical tag", len(missing)),
			Detail:         "Pages lacking rel=canonical leave duplicate-URL resolution to the search engine's guess.",
			Pages:          capPages(missing),
			Recommendation: "Add a self-referencing rel=canonical to every indexable page.",
			Falsifiability: "View source on each listed page: a <link rel=\"canonical\"> pointing at the preferred URL is present.",
		})
	}
	if len(brokenChain) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.canonicalization",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%d page(s) with a broken canonical target", len(brokenChain)),
			Detail:         "Canonicals that chain, point at redirects, or point at broken pages send conflicting index signals.",
			Pages:          capPages(brokenChain),
			Recommendation: "Point each canonical directly at a live, 200-status final URL.",
			Falsifiability: "Fetch each canonical target: it returns 200 with no further redirect or canonical hop.",
		})
	}
	if len(mismatchPairs) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.canonicalization",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d canonical URL(s) don't string-match the served URL", len(mismatchPairs)),
			Detail:         fmt.Sprintf("The canonical points at a trailing-slash/www/scheme variant of the same page (%s). Search engines usually reconcile it, but exact-match self-canonicals remove the ambiguity for free.", strings.Join(capPages(mismatchPairs), "; ")),
			Pages:          capPages(mismatchPages),
			Recommendation: "Emit the canonical as the exact served URL, byte-for-byte (same scheme, host, and trailing slash).",
			Falsifiability: "View source on each listed page: the rel=canonical href equals the served URL exactly.",
		})
	}

	// Mismatches weigh half — a consistency nit, not an indexing break.
	bad := float64(len(missing)+len(brokenChain)) + 0.5*float64(len(mismatchPairs))
	if assessed == 0 {
		// Nothing assessable — renormalize, never fabricate health.
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	return core.CheckResult{Score: fixedScore((float64(assessed) - bad) / float64(assessed) * 100), Findings: findings}, nil
}

// --- technical.indexability_robots ---

type technicalIndexabilityRobots struct{}

func (technicalIndexabilityRobots) ID() core.CheckID          { return "technical.indexability_robots" }
func (technicalIndexabilityRobots) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalIndexabilityRobots) Kind() CheckKind           { return KindDeterministic }
func (technicalIndexabilityRobots) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (technicalIndexabilityRobots) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var noindexed []string
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect {
			continue
		}
		assessed++
		if p.NoIndex {
			noindexed = append(noindexed, p.URL)
		}
	}

	var findings []core.Finding

	// A blanket "Disallow: /" is the single worst indexability defect a site
	// can ship (the staging-config-in-prod catastrophe) — it must never pass
	// silently. Read opportunistically off the deep pass.
	deep, hasDeep := in.Bundle.HTMLDeep()
	if hasDeep && deep.Robots.Found && deep.Robots.DisallowAll {
		findings = append(findings, core.Finding{
			CheckID:        "technical.indexability_robots",
			Severity:       core.SeverityCritical,
			Title:          "robots.txt disallows the entire site",
			Detail:         "The robots.txt rules block all crawlers from every path. Search engines cannot crawl anything — no page on the domain can rank, whatever the rest of this report says.",
			Recommendation: "Remove the blanket Disallow: / (usually a staging configuration that shipped to production) and re-verify in Search Console's robots tester.",
			Falsifiability: "GET /robots.txt no longer contains a User-agent: * group with Disallow: /.",
		})
		return core.CheckResult{Score: fixedScore(0), Findings: findings}, nil
	}

	if len(noindexed) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.indexability_robots",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%d internally-linked page(s) are noindexed", len(noindexed)),
			Detail:         "Pages the site links to internally carry a noindex directive — if intentional, they waste internal link equity; if not, they're invisible to search.",
			Pages:          capPages(noindexed),
			Recommendation: "Remove noindex from pages that should rank; stop linking prominently to pages that are deliberately noindexed.",
			Falsifiability: "Fetch each listed URL: no noindex appears in its meta robots tag or X-Robots-Tag header (or the page is delisted from internal navigation).",
		})
	}
	// The deep pass fetches robots.txt itself — when it found the file, the
	// crawl summary's missing flag is a provider gap, not a real absence.
	if !crawl.RobotsTxtFound && !(hasDeep && deep.Robots.Found) {
		findings = append(findings, core.Finding{
			CheckID:        "technical.indexability_robots",
			Severity:       core.SeverityLow,
			Title:          "No robots.txt found",
			Detail:         "The crawl found no robots.txt for the domain. Not fatal — everything defaults to crawlable — but crawl-budget and sitemap hints are lost.",
			Recommendation: "Serve a robots.txt that references the sitemap and scopes out non-content paths.",
			Falsifiability: "GET /robots.txt returns 200 with a Sitemap: line.",
		})
	}

	// Comment hygiene (deep-pass probe, read opportunistically — a missing
	// deep pass never skips the indexability core of this check). robots.txt
	// is public: engineering notes in it disclose the stack and its known
	// quirks to anyone probing the site. Zero score weight.
	if deep, ok := in.Bundle.HTMLDeep(); ok && len(deep.Robots.SuspectComments) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.indexability_robots",
			Severity:       core.SeverityInfo,
			Title:          "robots.txt comments read as internal engineering notes",
			Detail:         fmt.Sprintf("The public robots.txt carries comments like %q. No ranking impact — but it's free reconnaissance (stack, file paths, known bugs) for anyone who checks, and technically-literate visitors do.", deep.Robots.SuspectComments[0]),
			Recommendation: "Move the explanation into a code comment next to where the file is generated; reduce the public file to directives plus at most a one-line note.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}
	if deep, ok := in.Bundle.HTMLDeep(); ok && deep.Robots.HasHostDirective {
		findings = append(findings, core.Finding{
			CheckID:        "technical.indexability_robots",
			Severity:       core.SeverityInfo,
			Title:          "robots.txt carries a Host: directive, which Google and Bing ignore",
			Detail:         "Host: is a Yandex-only convention — for every other engine it's dead weight that makes the file read as copied boilerplate.",
			Recommendation: "Drop the Host: line unless Yandex is a real target market; the canonical host belongs in redirects and rel=canonical.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}
	// Sitemap-directive hygiene (zero score weight): duplicates read as
	// generator boilerplate; a live sitemap never declared in robots.txt is a
	// free hint left unstated.
	if deep, ok := in.Bundle.HTMLDeep(); ok && deep.Robots.Found {
		if deep.Robots.SitemapDirectiveCount >= 2 {
			findings = append(findings, core.Finding{
				CheckID:        "technical.indexability_robots",
				Severity:       core.SeverityInfo,
				Title:          fmt.Sprintf("robots.txt declares the sitemap %d times", deep.Robots.SitemapDirectiveCount),
				Detail:         "Multiple Sitemap: lines are harmless to crawlers but read as copy-paste debris — one declaration carries the whole signal.",
				Recommendation: "Clean the file to a single Sitemap: declaration.",
				Falsifiability: "This is informational; presence or absence changes no score.",
			})
		}
		if deep.Robots.SitemapDirectiveCount == 0 && deep.Sitemap.Found {
			findings = append(findings, core.Finding{
				CheckID:        "technical.indexability_robots",
				Severity:       core.SeverityInfo,
				Title:          "robots.txt doesn't declare the sitemap",
				Detail:         "A sitemap exists but robots.txt carries no Sitemap: line — the one standardized place every crawler checks for it.",
				Recommendation: fmt.Sprintf("Add: Sitemap: %s", deep.Sitemap.URL),
				Falsifiability: "This is informational; presence or absence changes no score.",
			})
		}
	}

	if assessed == 0 {
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	return core.CheckResult{Score: ratioScore(assessed-len(noindexed), assessed), Findings: findings}, nil
}

// --- technical.https ---

type technicalHTTPS struct{}

func (technicalHTTPS) ID() core.CheckID          { return "technical.https" }
func (technicalHTTPS) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalHTTPS) Kind() CheckKind           { return KindDeterministic }
func (technicalHTTPS) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (technicalHTTPS) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var httpPages, mixedPages []string
	assessed := 0
	for _, p := range crawl.Pages {
		// A redirect from http:// to https:// IS the correct setup — flagging
		// the hop as "served over plain HTTP" told users to do what they
		// already did. Unfetched pages (status 0) are not assessed.
		if p.IsRedirect || p.StatusCode == 0 {
			continue
		}
		assessed++
		if !p.IsHTTPS {
			httpPages = append(httpPages, p.URL)
		}
		if p.HTTPSToHTTPLinks {
			mixedPages = append(mixedPages, p.URL)
		}
	}

	// The certificate claim needs the provider to have actually assessed SSL
	// — a zero-value bool from an omitted ssl_info block is "not assessed",
	// and a false Critical "SSL certificate problem" on a healthy site is the
	// most trust-destroying output an audit can produce.
	certInvalid := crawl.CertificateAssessed && !crawl.ValidCertificate

	var findings []core.Finding
	if len(httpPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.https",
			Severity:       core.SeverityCritical,
			Title:          fmt.Sprintf("%d page(s) served over plain HTTP", len(httpPages)),
			Detail:         "Unencrypted pages are flagged by browsers and disadvantaged in ranking.",
			Pages:          capPages(httpPages),
			Recommendation: "Serve everything over HTTPS and 301 the HTTP variants.",
			Falsifiability: "Request each listed URL over http://: it 301s to https:// and the https response is valid.",
		})
	}
	if certInvalid {
		findings = append(findings, core.Finding{
			CheckID:        "technical.https",
			Severity:       core.SeverityCritical,
			Title:          "SSL certificate problem",
			Detail:         "The crawl reported an invalid certificate for the domain.",
			Recommendation: "Renew/fix the TLS certificate chain.",
			Falsifiability: "An SSL checker reports a valid, unexpired chain for the domain.",
		})
	}
	if len(mixedPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.https",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d HTTPS page(s) link to HTTP", len(mixedPages)),
			Detail:         "Secure pages linking to insecure URLs leak referrer data and send users to unencrypted pages (and often indicate insecure resources nearby).",
			Pages:          capPages(mixedPages),
			Recommendation: "Update in-page links and resources to https://.",
			Falsifiability: "Browser devtools shows zero mixed-content warnings on each listed page.",
		})
	}

	if assessed == 0 {
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	bad := len(httpPages) + len(mixedPages)
	score := ratioScore(assessed-bad, assessed)
	if certInvalid {
		score = fixedScore(score.Earned / 2)
	}
	return core.CheckResult{Score: score, Findings: findings}, nil
}

// --- technical.sitemap (reuses the site-level probe from the deep pass;
// discovery + quality — decision 5 kept sitemap analysis native) ---

type technicalSitemap struct{}

func (technicalSitemap) ID() core.CheckID          { return "technical.sitemap" }
func (technicalSitemap) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalSitemap) Kind() CheckKind           { return KindDeterministic }
func (technicalSitemap) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

// sitemapEntryCap mirrors the probe's MaxURLs — the coverage delta only runs
// below it (a capped entry list can't prove absence).
const sitemapEntryCap = 500

func (technicalSitemap) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	sm := deep.Sitemap

	if !sm.Found {
		return core.CheckResult{
			Score: fixedScore(0),
			Findings: []core.Finding{{
				CheckID:        "technical.sitemap",
				Severity:       core.SeverityHigh,
				Title:          "No XML sitemap found",
				Detail:         "None of the conventional sitemap locations answered with a parseable sitemap.",
				Recommendation: "Publish an XML sitemap at /sitemap.xml and declare it in robots.txt.",
				Falsifiability: "GET /sitemap.xml returns 200 with valid <urlset>/<sitemapindex> XML.",
			}},
		}, nil
	}

	// Legacy artifacts predate the per-entry detail: keep their original
	// scoring so old-run rechecks compare like-for-like.
	if sm.EntryCount > 0 && len(sm.Entries) == 0 {
		earned := 80.0
		var findings []core.Finding
		if sm.HasLastmod {
			earned += 20
		} else {
			findings = append(findings, noLastmodFinding(0, 0))
		}
		return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
	}

	// Quality uplift 1.3: found 40 · has entries 20 · lastmod coverage
	// prorated 40 (full credit at ≥90% — "one entry has a lastmod" is not
	// coverage).
	earned := 40.0
	var findings []core.Finding
	if sm.EntryCount > 0 {
		earned += 20
	} else {
		findings = append(findings, core.Finding{
			CheckID:        "technical.sitemap",
			Severity:       core.SeverityMedium,
			Title:          "Sitemap contains no URLs",
			Detail:         fmt.Sprintf("The sitemap at %s parsed but yielded zero page entries.", sm.URL),
			Recommendation: "Regenerate the sitemap so it lists every indexable page.",
			Falsifiability: "The sitemap lists at least the pages found by a site crawl.",
		})
	}
	if sm.EntryCount > 0 {
		coverage := float64(sm.LastmodCount) / float64(sm.EntryCount)
		if coverage >= 0.9 {
			earned += 40
		} else {
			earned += 40 * coverage
		}
		if missing := sm.EntryCount - sm.LastmodCount; missing > 0 {
			var uncovered []string
			for _, e := range sm.Entries {
				if e.LastMod == "" {
					uncovered = append(uncovered, e.URL)
				}
			}
			findings = append(findings, noLastmodFinding(missing, sm.EntryCount, uncovered...))
		}
	}
	if sm.PriorityCount > 0 || sm.ChangefreqCount > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "technical.sitemap",
			Severity:       core.SeverityInfo,
			Title:          "Sitemap emits <priority>/<changefreq>, which Google ignores",
			Detail:         fmt.Sprintf("%d entries carry <priority> and %d carry <changefreq>. Google has publicly stated it ignores both — <lastmod> is the field that matters. This costs nothing, it just isn't doing anything.", sm.PriorityCount, sm.ChangefreqCount),
			Recommendation: "Optional cleanup: drop priority/changefreq and invest in accurate lastmod values instead.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}

	// Suspiciously uniform lastmod: every entry stamped with the same date
	// reads as the generator writing "now" at build time — a value crawlers
	// learn to ignore as noise. Findings-only.
	if sm.LastmodCount >= 5 && sm.LastmodCount == sm.EntryCount && len(sm.Entries) > 0 {
		uniform := true
		first := ""
		for _, e := range sm.Entries {
			if e.LastMod == "" {
				continue
			}
			if first == "" {
				first = e.LastMod
			} else if e.LastMod != first {
				uniform = false
				break
			}
		}
		if uniform && first != "" {
			findings = append(findings, core.Finding{
				CheckID:        "technical.sitemap",
				Severity:       core.SeverityInfo,
				Title:          "Every sitemap entry carries the identical <lastmod>",
				Detail:         fmt.Sprintf("All %d entries share the same lastmod (%s) — the signature of a generator stamping build time instead of real content dates. A lastmod that always says \"now\" gets ignored as noise.", sm.EntryCount, first),
				Recommendation: "Source lastmod from each page's actual last content change, not the deploy timestamp.",
				Falsifiability: "This is informational; presence or absence changes no score.",
			})
		}
	}

	// Coverage delta: crawled 200-status pages absent from the sitemap. Only
	// when the entry list wasn't truncated — a capped list can't prove absence.
	// The crawl is read opportunistically (not in Requires) so a scoped
	// re-check of this check keeps working without a crawl artifact.
	if crawl, ok := in.Bundle.Crawl(); ok && sm.EntryCount > 0 && sm.EntryCount < sitemapEntryCap {
		inSitemap := map[string]bool{}
		for _, e := range sm.Entries {
			inSitemap[normalizeCheckURL(e.URL)] = true
		}
		var absent []string
		for _, p := range crawl.Pages {
			if p.StatusCode != 200 || p.IsRedirect || p.NoIndex {
				continue
			}
			// Query-string URLs (pagination, filters) don't belong in a
			// sitemap — telling users to add them was backwards advice.
			if u, err := url.Parse(p.URL); err == nil && u.RawQuery != "" {
				continue
			}
			if !inSitemap[normalizeCheckURL(p.URL)] {
				absent = append(absent, p.URL)
			}
		}
		if len(absent) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "technical.sitemap",
				Severity:       core.SeverityLow,
				Title:          fmt.Sprintf("%d crawled page(s) missing from the sitemap", len(absent)),
				Detail:         "Indexable pages the crawler reached through internal links don't appear in the sitemap — they still get crawled, but lose the freshness/priority hints the sitemap exists to provide.",
				Pages:          capPages(absent),
				Recommendation: "Regenerate the sitemap to include every indexable page.",
				Falsifiability: "Each listed URL appears in the sitemap on re-fetch.",
			})
		}
	}

	// Audit-coverage disclosure (gap-closure round 4): when the crawl stopped
	// at its page cap, whole site sections listed in the sitemap may never
	// have been fetched — and every page-level check silently reads as "no
	// findings" for them. Name the unexamined sections so absence of findings
	// is never mistaken for health.
	if crawl, ok := in.Bundle.Crawl(); ok && crawl.PageCap > 0 && crawl.PagesCrawled >= crawl.PageCap && len(sm.Entries) > 0 {
		crawledSections := map[string]bool{}
		for _, p := range crawl.Pages {
			crawledSections[urlFirstSegment(p.URL)] = true
		}
		uncoveredCount := map[string]int{}
		uncoveredSample := map[string]string{}
		for _, e := range sm.Entries {
			s := urlFirstSegment(e.URL)
			if s == "" || crawledSections[s] {
				continue
			}
			uncoveredCount[s]++
			if _, ok := uncoveredSample[s]; !ok {
				uncoveredSample[s] = e.URL
			}
		}
		if len(uncoveredCount) > 0 {
			var sections []string
			var samplePages []string
			for s, n := range uncoveredCount {
				sections = append(sections, fmt.Sprintf("/%s/* (%d URL(s))", s, n))
				samplePages = append(samplePages, uncoveredSample[s])
			}
			sort.Strings(sections)
			sort.Strings(samplePages)
			findings = append(findings, core.Finding{
				CheckID:  "technical.sitemap",
				Severity: core.SeverityInfo,
				Title:    fmt.Sprintf("Crawl page cap reached — %d site section(s) were never crawled", len(uncoveredCount)),
				Detail: fmt.Sprintf("The crawl stopped at its %d-page cap before reaching any page under: %s. Page-level findings and scores in this report do NOT cover those sections — no findings there means unexamined, not healthy.",
					crawl.PageCap, strings.Join(sections, ", ")),
				Pages:          capPages(samplePages),
				Recommendation: "Re-run the audit with a higher page cap, or audit the uncovered sections individually.",
				Falsifiability: "A re-run's crawl includes at least one page from each listed section.",
			})
		}
	}

	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}

// urlFirstSegment returns the lowercased first path segment ("" for the
// root), the section key for coverage comparisons.
func urlFirstSegment(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	return strings.ToLower(segs[0])
}

// noLastmodFinding covers both the zero-lastmod and partial-coverage cases.
func noLastmodFinding(missing, total int, uncovered ...string) core.Finding {
	title := "Sitemap entries carry no <lastmod>"
	detail := "Without lastmod dates, crawlers can't prioritize fresh content from the sitemap."
	if missing > 0 && missing < total {
		title = fmt.Sprintf("%d of %d sitemap entries lack <lastmod>", missing, total)
		detail = "Partial lastmod coverage — often the static pages the generator skips — means crawlers can't trust the sitemap's freshness signal for the uncovered URLs."
	}
	return core.Finding{
		CheckID:        "technical.sitemap",
		Severity:       core.SeverityLow,
		Title:          title,
		Detail:         detail,
		Pages:          capPages(uncovered),
		Recommendation: "Emit accurate <lastmod> values on every entry, updated when the page changes.",
		Falsifiability: "Every sitemap entry includes a <lastmod> timestamp that updates when the page does.",
	}
}

// --- technical.indexnow (decision 11) ---

type technicalIndexNow struct{}

func (technicalIndexNow) ID() core.CheckID          { return "technical.indexnow" }
func (technicalIndexNow) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalIndexNow) Kind() CheckKind           { return KindDeterministic }
func (technicalIndexNow) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (technicalIndexNow) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	if deep.IndexNow.KeyFileFound {
		// On a soft-404 site every path answers 200 — "found" proves nothing.
		if deep.Soft404.Fetched && deep.Soft404.FinalStatus == 200 {
			return core.CheckResult{Score: nil}, nil
		}
		return core.CheckResult{Score: fixedScore(100)}, nil
	}
	// Absence is only detectable at the conventional path — a site running
	// IndexNow with the documented random-key filename is indistinguishable
	// from one without it. An unverifiable absence must nudge, never score
	// 0/100: findings-only, and the category renormalizes.
	return core.CheckResult{
		Score: nil,
		Findings: []core.Finding{{
			CheckID:        "technical.indexnow",
			Severity:       core.SeverityLow,
			Title:          "No IndexNow key file detected",
			Detail:         "No key file was found at the conventional /indexnow.txt location. IndexNow keys can live at any /<key>.txt path, so this only proves the conventional setup is absent.",
			Recommendation: "Adopt IndexNow (Bing/Yandex instant indexing): host a key file and ping the endpoint on publish/update.",
			Falsifiability: "A key file exists at https://<domain>/<key>.txt and an IndexNow submission for a fresh URL returns HTTP 200/202.",
		}},
	}, nil
}
