package checks

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// fetchedDeepPages returns the sampled pages the deep pass actually fetched
// — blocked pages are per-page constraints (§8.3), never counted against the
// content score.
func fetchedDeepPages(in Input) []artifacts.DeepPage {
	deep, _ := in.Bundle.HTMLDeep()
	var out []artifacts.DeepPage
	for _, p := range deep.Pages {
		if p.Fetched {
			out = append(out, p)
		}
	}
	return out
}

// --- content.readability (Flesch + sentence/paragraph lengths) ---

type contentReadability struct{}

func (contentReadability) ID() core.CheckID          { return "content.readability" }
func (contentReadability) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentReadability) Kind() CheckKind           { return KindDeterministic }
func (contentReadability) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentReadability) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	passing := 0
	var hardPages, wallPages []string
	for _, p := range pages {
		ok := true
		// Flesch 0 is the unassessed sentinel; genuinely negative scores
		// (dense jargon) still count as hard.
		if p.FleschReadingEase != 0 && p.FleschReadingEase < 40 {
			ok = false
			hardPages = append(hardPages, p.URL)
		}
		// Landing copy made of punctuation-light fragments collapses into one
		// giant "sentence" — the average is only meaningful with a real
		// sentence base.
		if p.SentenceCount >= 5 && p.AvgSentenceWords > 28 {
			ok = false
			if len(hardPages) == 0 || hardPages[len(hardPages)-1] != p.URL {
				hardPages = append(hardPages, p.URL)
			}
		}
		if p.ParagraphCount > 0 && p.LongParagraphCount*2 > p.ParagraphCount {
			ok = false
			wallPages = append(wallPages, p.URL)
		}
		if ok {
			passing++
		}
	}

	var findings []core.Finding
	if len(hardPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.readability",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) read as difficult", len(hardPages)),
			Detail:         "Flesch reading ease below 40 or average sentences beyond ~28 words push readers (and snippet extraction) away.",
			Pages:          capPages(hardPages),
			Recommendation: "Shorten sentences and split dense passages; aim for Flesch 50+.",
			Falsifiability: "Each listed page scores Flesch ≥ 40 with average sentence length ≤ 28 words.",
		})
	}
	if len(wallPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.readability",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) are walls of text", len(wallPages)),
			Detail:         "Most paragraphs run past ~150 words, which suppresses scanning and extraction.",
			Pages:          capPages(wallPages),
			Recommendation: "Break paragraphs at one idea each (2–4 sentences), add subheadings and lists.",
			Falsifiability: "Fewer than half of each listed page's paragraphs exceed 150 words.",
		})
	}

	return core.CheckResult{Score: ratioScore(passing, len(pages)), Findings: findings}, nil
}

// --- content.substance (word-count floors, info density ※) ---

type contentSubstance struct{}

func (contentSubstance) ID() core.CheckID          { return "content.substance" }
func (contentSubstance) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentSubstance) Kind() CheckKind           { return KindDeterministic }
func (contentSubstance) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentSubstance) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	passing := 0
	assessed := 0
	var thinPages []string
	evidence := map[string]any{}
	perPage := map[string]any{}
	for _, p := range pages {
		// Utility/CTA pages (booking, login, ...) are legitimately short.
		if isUtilityPage(p.URL) {
			continue
		}
		assessed++
		floor := 300
		if p.IsHomepage || isListingIndexPage(p.URL) {
			floor = 100 // navigational surfaces, not articles
		}
		if p.WordCount >= floor {
			passing++
		} else {
			thinPages = append(thinPages, p.URL)
		}
		perPage[p.URL] = map[string]any{
			"wordCount":       p.WordCount,
			"uniqueWordRatio": p.UniqueWordRatio,
		}
	}
	evidence["pages"] = perPage

	var findings []core.Finding
	if len(thinPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.substance",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d sampled page(s) below the word-count floor", len(thinPages)),
			Detail:         "Sampled content pages run under ~300 words — too little substance to earn rankings or citations.",
			Pages:          capPages(thinPages),
			Recommendation: "Expand each page with genuinely useful depth (data, examples, answers), or fold it into a stronger page.",
			Falsifiability: "Each listed page measures ≥ 300 words of body text (homepage exempt).",
		})
	}

	return core.CheckResult{Score: ratioScore(passing, assessed), Findings: findings, Evidence: evidence}, nil
}

// --- content.keyword_density ---

type contentKeywordDensity struct{}

func (contentKeywordDensity) ID() core.CheckID          { return "content.keyword_density" }
func (contentKeywordDensity) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentKeywordDensity) Kind() CheckKind           { return KindDeterministic }
func (contentKeywordDensity) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentKeywordDensity) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	passing, assessed := 0, 0
	var stuffed []string
	for _, p := range pages {
		// Homepages and listing indexes repeat their brand/category term by
		// construction — density there is navigation, not manipulation.
		if p.IsHomepage || isListingIndexPage(p.URL) {
			continue
		}
		assessed++
		// > 4% for the single most frequent non-stopword reads as stuffing.
		if p.TopKeywordDensityPct > 4.0 && p.WordCount >= 200 {
			stuffed = append(stuffed, p.URL)
		} else {
			passing++
		}
	}

	var findings []core.Finding
	if len(stuffed) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.keyword_density",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d sampled page(s) show keyword stuffing", len(stuffed)),
			Detail:         "The single most frequent term exceeds 4% of all words — repetition at that rate reads as manipulation.",
			Pages:          capPages(stuffed),
			Recommendation: "Vary phrasing naturally; write for the topic, not the token.",
			Falsifiability: "No single non-stopword exceeds 4% of each listed page's word count.",
		})
	}

	return core.CheckResult{Score: ratioScore(passing, assessed), Findings: findings}, nil
}

// --- content.internal_linking (body-scoped links + island detection —
// quality uplift 1.1: the crawl's page-wide count includes nav/footer chrome,
// so every site with a navbar passed while its posts sat orphaned) ---

type contentInternalLinking struct{}

func (contentInternalLinking) ID() core.CheckID          { return "content.internal_linking" }
func (contentInternalLinking) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentInternalLinking) Kind() CheckKind           { return KindDeterministic }
func (contentInternalLinking) Requires() []core.Kind {
	return []core.Kind{artifacts.KindCrawl, artifacts.KindHTMLDeep}
}

// hubAndSpokeRecommendation is the generic prescription for orphaned content.
const hubAndSpokeRecommendation = "Build a hub-and-spoke structure: add 2–3 contextual in-body links to sibling articles plus 1 link to a hub/pillar page per post, with descriptive anchor text, and link your top posts from the homepage."

func (contentInternalLinking) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	// Assessed set: sampled non-homepage pages with substance, extracted with
	// body-scoped link data. Listing indexes (/blog, /articles, ...) are
	// excluded — their "body" is the post list itself, so they pass trivially
	// and used to defeat the island detector while every actual post sat
	// orphaned. Old artifacts (predating the fields) fall back to the legacy
	// crawl-wide heuristic below.
	var assessedPages []artifacts.DeepPage
	var homepage *artifacts.DeepPage
	for _, p := range fetchedDeepPages(in) {
		if p.IsHomepage {
			hp := p
			homepage = &hp
			continue
		}
		if !p.BodyLinksAssessed || p.WordCount < 300 || isListingIndexPage(p.URL) {
			continue
		}
		assessedPages = append(assessedPages, p)
	}

	var findings []core.Finding
	var score *core.Score

	if len(assessedPages) > 0 {
		passing := 0
		var underLinked []string
		crossContentAnywhere := false
		for _, p := range assessedPages {
			hasContentTarget := false
			for _, target := range p.BodyInternalTargets {
				if isContentPageTarget(target, p.URL) {
					hasContentTarget = true
					crossContentAnywhere = true
					break
				}
			}
			if p.BodyInternalLinkCount >= 2 && hasContentTarget {
				passing++
			} else {
				underLinked = append(underLinked, p.URL)
			}
		}
		score = ratioScore(passing, len(assessedPages))

		// Island finding: the sampled content set links to NOTHING outside
		// itself — a closed island no link equity flows through.
		if len(assessedPages) >= 3 && !crossContentAnywhere {
			var pages []string
			for _, p := range assessedPages {
				pages = append(pages, p.URL)
			}
			findings = append(findings, core.Finding{
				CheckID:        "content.internal_linking",
				Severity:       core.SeverityHigh,
				Title:          "Content pages form a closed island — no post links to any other post",
				Detail:         fmt.Sprintf("Across %d sampled content pages, the article bodies contain zero links to any other content page. Navigation chrome doesn't count: crawlers weight in-content contextual links, and without them every post is an orphan competing alone.", len(assessedPages)),
				Pages:          capPages(pages),
				Recommendation: hubAndSpokeRecommendation,
				Falsifiability: "Each sampled article's body (not nav/footer) contains at least one link to another content page.",
			})
		} else if len(underLinked) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "content.internal_linking",
				Severity:       core.SeverityMedium,
				Title:          fmt.Sprintf("%d content page(s) lack in-body internal links", len(underLinked)),
				Detail:         "These pages carry fewer than 2 in-body internal links, or none pointing at another content page — nav links don't distribute topical authority.",
				Pages:          capPages(underLinked),
				Recommendation: hubAndSpokeRecommendation,
				Falsifiability: "Each listed page's article body carries ≥2 internal links, at least one to another content page.",
			})
		}
	} else {
		// Legacy fallback (old artifacts only): the crawl-wide density check.
		passing, assessed := 0, 0
		var isolated []string
		for _, p := range crawl.Pages {
			if p.StatusCode != 200 || p.IsRedirect || p.WordCount < 200 {
				continue
			}
			assessed++
			per1000 := float64(p.InternalLinksCount) / float64(p.WordCount) * 1000
			if per1000 >= 1 {
				passing++
			} else {
				isolated = append(isolated, p.URL)
			}
		}
		score = ratioScore(passing, assessed)
		if len(isolated) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "content.internal_linking",
				Severity:       core.SeverityLow,
				Title:          fmt.Sprintf("%d page(s) are internally under-linked", len(isolated)),
				Detail:         "Content pages carrying fewer than ~1 internal link per 1000 words strand link equity and topical context.",
				Pages:          capPages(isolated),
				Recommendation: hubAndSpokeRecommendation,
				Falsifiability: "Each listed page carries at least 1 internal link per 1000 words of body text.",
			})
		}
	}

	// Homepage signal: the highest-authority page linking no individual
	// content page keeps every post at depth ≥2 living off the listing page's
	// scraps. Findings-only — the score stays a property of the content pages.
	if homepage != nil && homepage.BodyLinksAssessed && len(assessedPages) >= 3 {
		homepageLinksContent := false
		for _, target := range homepage.BodyInternalTargets {
			if isContentPageTarget(target, homepage.URL) {
				homepageLinksContent = true
				break
			}
		}
		if !homepageLinksContent {
			findings = append(findings, core.Finding{
				CheckID:        "content.internal_linking",
				Severity:       core.SeverityLow,
				Title:          "Homepage links to no individual content page",
				Detail:         "The homepage is the site's highest-authority page, but its body links only to section indexes — every article depends on the listing page for equity and discovery.",
				Pages:          []string{homepage.URL},
				Recommendation: "Link 3–5 top articles directly from the homepage with descriptive anchor text (a \"featured\"/\"latest\" block works).",
				Falsifiability: "The homepage body contains direct links to individual content pages, not just the section index.",
			})
		}
	}

	// Structural crawl-graph signals (gap-closure round 4 — the case this
	// check scored 100 on: every post reachable only through paginated
	// listing pages, sitting many clicks deep, with the body-scoped sample
	// containing no actual articles). Pagination mirrors (?page=N duplicates
	// of the listing) inflate a post's inbound count without adding a real
	// second path, so when the crawl contains pagination URLs the thin-inlink
	// threshold rises to ≤2.
	crawlHasPagination := false
	inboundPopulated, depthPopulated := false, false
	for _, p := range crawl.Pages {
		if isPaginationURL(p.URL) {
			crawlHasPagination = true
		}
		if p.InboundLinksCount > 0 {
			inboundPopulated = true
		}
		if p.ClickDepth > 0 {
			depthPopulated = true
		}
	}
	inboundThreshold := 1
	if crawlHasPagination {
		inboundThreshold = 2
	}
	contentPages, thinlyLinked, deepPages := 0, []string{}, []string{}
	// Legacy artifacts may carry no inbound/depth data at all — a zero field
	// then means "not assessed", and the structural signals must stay silent
	// rather than read every page as an orphan.
	if inboundPopulated && depthPopulated {
		for _, p := range crawl.Pages {
			if p.StatusCode != 200 || p.IsRedirect || p.WordCount < 300 ||
				isListingIndexPage(p.URL) || isPaginationURL(p.URL) || p.ClickDepth == 0 {
				continue
			}
			contentPages++
			if p.ClickDepth >= 2 && p.InboundLinksCount <= inboundThreshold {
				thinlyLinked = append(thinlyLinked, p.URL)
			}
			// Docs trees are legitimately deep hierarchies — exempt them.
			if p.ClickDepth >= 4 && !isDocsClusterURL(p.URL) {
				deepPages = append(deepPages, p.URL)
			}
		}
	}
	thinShare := share(len(thinlyLinked), contentPages)
	if len(thinlyLinked) > 0 && contentPages > 0 {
		severity := core.SeverityLow
		detail := "Deeper content pages with almost no inbound internal links depend on very few paths (often just a listing or pagination page) for discovery and equity."
		if thinShare >= 0.5 && contentPages >= 5 {
			severity = core.SeverityMedium
			detail = fmt.Sprintf("%d of %d crawled content pages carry at most %d inbound internal link(s) — consistent with discovery hanging off listing/pagination pages alone, the weakest structure there is for link equity.", len(thinlyLinked), contentPages, inboundThreshold)
		}
		findings = append(findings, core.Finding{
			CheckID:        "content.internal_linking",
			Severity:       severity,
			Title:          fmt.Sprintf("%d content page(s) have almost no inbound internal links", len(thinlyLinked)),
			Detail:         detail,
			Pages:          capPages(thinlyLinked),
			Recommendation: "Add related-post modules and contextual in-body links so every article has 2+ inbound paths beyond the listing pages.",
			Falsifiability: "A re-crawl shows each listed page with 3+ inbound internal links from non-pagination pages.",
		})
	}
	if len(deepPages) > 0 && contentPages > 0 && share(len(deepPages), contentPages) >= 0.3 {
		findings = append(findings, core.Finding{
			CheckID:        "content.internal_linking",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d content page(s) sit 4+ clicks from the homepage", len(deepPages)),
			Detail:         "Content buried this deep in the click graph receives almost no crawl priority or link equity — paginated archives are usually the cause.",
			Pages:          capPages(deepPages),
			Recommendation: "Flatten the architecture: bigger listing pages, category hubs linked from the homepage, and related-post links between articles.",
			Falsifiability: "A re-crawl shows the listed pages within 3 clicks of the homepage.",
		})
	}

	// The crawl-graph structure caps the score: a site whose posts hang off
	// pagination threads is not "well internally linked" no matter how the
	// sampled bodies score.
	evidence := map[string]any{}
	if contentPages >= 5 && score != nil {
		// Caps engage only at clear-majority shares on a meaningful page base
		// — a healthy small blog whose posts carry listing+related (2 inbound)
		// links must not be crushed by the pagination-adjusted threshold.
		scoreCap := 100.0
		if thinShare >= 0.8 || share(len(deepPages), contentPages) >= 0.6 {
			scoreCap = 30
		} else if thinShare >= 0.5 || share(len(deepPages), contentPages) >= 0.3 {
			scoreCap = 60
		}
		if score.Earned > scoreCap {
			score = &core.Score{Earned: scoreCap, Possible: score.Possible}
		}
		evidence["crawlContentPages"] = contentPages
		evidence["singlePathShare"] = thinShare
	}
	// Strength gate (round 4): a perfect score earned from fewer than 3
	// assessed article bodies is absence of evidence, not evidence of health —
	// it must never surface as a report strength.
	if len(assessedPages) < 3 {
		evidence["strengthIneligible"] = true
	}

	return core.CheckResult{Score: score, Findings: findings, Evidence: evidence}, nil
}

// contentSectionSlugs are the first-path-segment names of content-section
// indexes — pages whose body IS the item list. Singular and plural forms both
// appear in the wild ("/blog" and "/blogs"); missing the plural once let a
// /blogs listing stand in as a "content page", pass the link checks trivially
// (its body is nothing but post links), and defeat the island detector while
// every actual post sat orphaned.
var contentSectionSlugs = map[string]bool{
	"blog": true, "blogs": true, "article": true, "articles": true,
	"news": true, "post": true, "posts": true, "resource": true,
	"resources": true, "insight": true, "insights": true, "guide": true,
	"guides": true, "academy": true, "learn": true, "library": true,
	"glossary": true, "stories": true, "journal": true, "updates": true,
	"docs": true, "documentation": true, "press": true, "media": true,
}

// utilityPageSlugs are single-segment utility/CTA pages that never count as
// content-link targets and are never held to article standards.
var utilityPageSlugs = map[string]bool{
	"about": true, "about-us": true, "contact": true, "contact-us": true,
	"pricing": true, "privacy": true, "privacy-policy": true,
	"terms": true, "terms-of-service": true, "faq": true, "faqs": true,
	"demo": true, "book-a-demo": true, "request-demo": true, "get-started": true,
	"features": true, "product": true, "products": true, "integrations": true,
	"login": true, "sign-in": true, "signup": true, "sign-up": true,
	"register": true, "cart": true, "checkout": true, "thank-you": true,
}

// isUtilityPage reports whether a URL is a single-segment utility/CTA page.
func isUtilityPage(pageURL string) bool {
	u, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 1 {
		return utilityPageSlugs[strings.ToLower(segs[0])]
	}
	return false
}

// isArticleLike separates articles/content items from nav, landing, and
// utility pages. Article-standard assertions — bylines, publish dates, word
// floors — must only ever be applied to pages that are plausibly articles;
// flagging a booking page for "no author byline" was the historical FP class
// this gate closes.
func isArticleLike(p artifacts.DeepPage) bool {
	if p.IsHomepage || isListingIndexPage(p.URL) || isUtilityPage(p.URL) {
		return false
	}
	return p.HasArticleTag || p.HasByline || p.HasDates || urlClusterKey(p.URL) != ""
}

// isContentPageTarget reports whether a body-internal link target counts as
// "another content page" — not the homepage, not a bare section index, not
// the page itself.
func isContentPageTarget(target, pageURL string) bool {
	tu, err := url.Parse(target)
	if err != nil {
		return false
	}
	path := strings.Trim(tu.Path, "/")
	if path == "" {
		return false // homepage
	}
	if segs := strings.Split(path, "/"); len(segs) == 1 {
		slug := strings.ToLower(segs[0])
		if contentSectionSlugs[slug] || utilityPageSlugs[slug] {
			return false // listing/utility indexes, not content
		}
	}
	return normalizeCheckURL(target) != normalizeCheckURL(pageURL)
}

// isDocsClusterURL exempts documentation trees from the click-depth signal —
// /docs/a/b/c at depth 4+ is a legitimate hierarchy, not a stranded archive.
func isDocsClusterURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 {
		return false
	}
	switch strings.ToLower(segs[0]) {
	case "docs", "documentation", "help", "reference", "manual", "kb", "knowledge-base":
		return true
	}
	return false
}

// isListingIndexPage reports whether a URL is a bare content-section index
// (/blog, /blogs, /articles, ...) — a page whose body is the post list
// itself, which must not stand in for a content page in link-graph
// assessments. Pagination query variants (/blogs?page=2) share the path and
// classify identically.
func isListingIndexPage(pageURL string) bool {
	u, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	path := strings.Trim(u.Path, "/")
	if segs := strings.Split(path, "/"); len(segs) == 1 {
		return contentSectionSlugs[strings.ToLower(segs[0])]
	}
	return false
}

// isPaginationURL flags listing-pagination variants (?page=2, ?x_page=1,
// /page/3) — near-duplicate mirrors that inflate a post's inbound-link count
// without adding a real second discovery path. The param match is anchored
// to a whole "...page" parameter name so ?homepage=true can't count.
func isPaginationURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if pageParamRe.MatchString(strings.ToLower(u.RawQuery)) {
		return true
	}
	return pagePathRe.MatchString(strings.ToLower(u.Path))
}

var (
	pageParamRe = regexp.MustCompile(`(?:^|&)(?:[a-z0-9_-]*_)?page=\d`)
	pagePathRe  = regexp.MustCompile(`/page/\d+/?$`)
)

// --- content.freshness ---

type contentFreshness struct{}

func (contentFreshness) ID() core.CheckID          { return "content.freshness" }
func (contentFreshness) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentFreshness) Kind() CheckKind           { return KindDeterministic }
func (contentFreshness) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentFreshness) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	dated, stale := 0, 0
	var undatedPages, stalePages []string
	twoYearsAgo := time.Now().AddDate(-2, 0, 0)
	for _, p := range pages {
		// Freshness is an article property — a booking page or FAQ hub
		// showing no publish date is normal, not a defect. Undated pages can
		// still be article-like via their URL cluster (CMS collection paths).
		if !isArticleLike(p) {
			continue
		}
		if !p.HasDates {
			undatedPages = append(undatedPages, p.URL)
			continue
		}
		dated++
		newest := p.ModifiedDate
		if newest == "" {
			newest = p.PublishedDate
		}
		if t, err := time.Parse("2006-01-02", newest); err == nil && t.Before(twoYearsAgo) {
			stale++
			stalePages = append(stalePages, p.URL)
		}
	}

	var findings []core.Finding
	if len(undatedPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.freshness",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) show no publish/update date", len(undatedPages)),
			Detail:         "Undated content can't demonstrate freshness to readers, crawlers, or AI answer engines.",
			Pages:          capPages(undatedPages),
			Recommendation: "Show a visible published/updated date and mirror it in article schema (datePublished/dateModified).",
			Falsifiability: "Each listed page displays a date and its schema carries datePublished/dateModified.",
		})
	}
	if len(stalePages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.freshness",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) untouched for 2+ years", len(stalePages)),
			Detail:         "Sampled content shows no update in over two years.",
			Pages:          capPages(stalePages),
			Recommendation: "Refresh stale high-value pages (facts, screenshots, links) and stamp the new dateModified.",
			Falsifiability: "Each listed page's dateModified is within the last two years after the refresh.",
		})
	}

	assessed := len(undatedPages) + dated
	if assessed == 0 {
		// No article-like page in the sample — freshness was not assessed.
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	passing := dated - stale
	return core.CheckResult{Score: ratioScore(passing, assessed), Findings: findings}, nil
}

// --- content.trust_signals (byline/about/contact, citation markers ※) ---

type contentTrustSignals struct{}

func (contentTrustSignals) ID() core.CheckID          { return "content.trust_signals" }
func (contentTrustSignals) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentTrustSignals) Kind() CheckKind           { return KindDeterministic }
func (contentTrustSignals) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentTrustSignals) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	bylines, cited, articles := 0, 0, 0
	siteHasAbout, siteHasContact := false, false
	var noBylinePages, noCitationPages, uncitedLongform []string
	citationDomains := map[string]any{}
	for _, p := range pages {
		if p.LinksAboutPage {
			siteHasAbout = true
		}
		if p.LinksContact {
			siteHasContact = true
		}
		// Bylines and citations are article properties — judging a booking
		// page for "no author byline" was the historical FP class.
		if !isArticleLike(p) {
			continue
		}
		articles++
		if p.HasByline {
			bylines++
		} else {
			noBylinePages = append(noBylinePages, p.URL)
		}
		// Quality uplift 1.2: "cited" means body-scoped external links minus
		// CTA/social domains — footer socials and wa.me buttons are not
		// citations. Old artifacts (no body extraction) keep the raw count.
		pageCited := p.ExternalLinkCount > 0
		if p.BodyLinksAssessed {
			pageCited = p.BodyCitationCount > 0
			if len(p.BodyCitationDomains) > 0 {
				citationDomains[p.URL] = p.BodyCitationDomains
			}
		}
		if pageCited {
			cited++
		} else {
			noCitationPages = append(noCitationPages, p.URL)
			if p.BodyLinksAssessed && p.WordCount >= 1500 {
				uncitedLongform = append(uncitedLongform, p.URL)
			}
		}
	}

	evidence := map[string]any{
		"articlesSampled": articles,
		"siteHasAbout":    siteHasAbout,
		"siteHasContact":  siteHasContact,
	}
	// share(0,0)=1 would record bylineShare 1.0 "as hard evidence" for a
	// sample containing zero articles — omit the shares instead of lying.
	if articles > 0 {
		evidence["bylineShare"] = share(bylines, articles)
		evidence["citationShare"] = share(cited, articles)
	}
	if len(citationDomains) > 0 {
		evidence["citationDomains"] = citationDomains
	}

	var findings []core.Finding
	if articles > 0 && bylines == 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.trust_signals",
			Severity:       core.SeverityMedium,
			Title:          "No author bylines on sampled content",
			Detail:         "None of the sampled articles names an author — the most direct Experience/Expertise signal in E-E-A-T.",
			Pages:          capPages(noBylinePages),
			Recommendation: "Add a real author byline (name + short bio/credential link) to every article.",
			Falsifiability: "Each sampled article renders a visible author name, mirrored in its schema's author field.",
		})
	}
	if !siteHasAbout || !siteHasContact {
		findings = append(findings, core.Finding{
			CheckID:        "content.trust_signals",
			Severity:       core.SeverityLow,
			Title:          "About/Contact pages not discoverable from sampled pages",
			Detail:         "Trust evaluation (human and algorithmic) looks for who is behind the site; the sample never links About or Contact.",
			Recommendation: "Link About and Contact from the global footer/navigation.",
			Falsifiability: "Every page's footer links to reachable About and Contact pages.",
		})
	}
	if articles > 0 && cited*2 < articles {
		findings = append(findings, core.Finding{
			CheckID:        "content.trust_signals",
			Severity:       core.SeverityLow,
			Title:          "Sampled content rarely cites external sources",
			Detail:         "Most sampled articles link to no outside source in their body (social/CTA links don't count) — claims without citations read as unverified.",
			Pages:          capPages(noCitationPages),
			Recommendation: "Cite primary sources for factual claims (studies, docs, data).",
			Falsifiability: "At least half the sampled articles carry one or more in-body external citations.",
		})
	}
	if len(uncitedLongform) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.trust_signals",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d long-form page(s) cite no sources", len(uncitedLongform)),
			Detail:         "Articles of 1500+ words making claims with zero outbound citations read as unverifiable — the exact profile both human trust evaluation and AI answer engines discount.",
			Pages:          capPages(uncitedLongform),
			Recommendation: "Add 2–5 citations to primary sources (studies, official docs, first-party data) in each long-form piece.",
			Falsifiability: "Each listed page's body carries at least one external citation to a non-social source.",
		})
	}

	// Weighted: bylines 40, about/contact 30 (15 each), citations 30. With
	// zero article-like pages sampled, only the about/contact component was
	// assessed — score that slice on its own basis instead of gifting the
	// byline/citation points.
	aboutContact := 0.0
	if siteHasAbout {
		aboutContact += 15
	}
	if siteHasContact {
		aboutContact += 15
	}
	if articles == 0 {
		return core.CheckResult{Score: fixedScore(aboutContact / 30 * 100), Findings: findings, Evidence: evidence}, nil
	}
	earned := share(bylines, articles)*40 + share(cited, articles)*30 + aboutContact
	return core.CheckResult{Score: fixedScore(earned), Findings: findings, Evidence: evidence}, nil
}

func share(n, total int) float64 {
	if total <= 0 {
		return 1
	}
	return float64(n) / float64(total)
}

// --- content.filler_ai_patterns (filler + AI-pattern + repetition scorers ※,
// decision 11 — computed in Go, fed to the rubric as hard evidence) ---

type contentFillerAIPatterns struct{}

func (contentFillerAIPatterns) ID() core.CheckID          { return "content.filler_ai_patterns" }
func (contentFillerAIPatterns) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentFillerAIPatterns) Kind() CheckKind           { return KindDeterministic }
func (contentFillerAIPatterns) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentFillerAIPatterns) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	passing, assessed := 0, 0
	var flagged []string
	perPage := map[string]any{}
	for _, p := range pages {
		// A per-1000-word rate on a 120-word page flags a single "in order
		// to" — the signal needs a real text base.
		if p.WordCount < 300 {
			continue
		}
		assessed++
		per1000 := float64(p.FillerPhraseCount+p.AIPatternCount) / float64(p.WordCount) * 1000
		ok := per1000 <= 5 && p.RepeatedSentencePct <= 10
		if ok {
			passing++
		} else {
			flagged = append(flagged, p.URL)
		}
		perPage[p.URL] = map[string]any{
			"fillerPhrases":       p.FillerPhraseCount,
			"aiPatternPhrases":    p.AIPatternCount,
			"repeatedSentencePct": p.RepeatedSentencePct,
		}
	}

	var findings []core.Finding
	if len(flagged) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.filler_ai_patterns",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d sampled page(s) dense with filler/AI-pattern phrasing", len(flagged)),
			Detail:         "High rates of stock transitions (\"in today's fast-paced world\", \"delve into\", \"it's important to note\") and repeated sentences read as low-effort generation.",
			Pages:          capPages(flagged),
			Recommendation: "Edit out filler openers and templated transitions; every sentence should carry information.",
			Falsifiability: "Each listed page measures ≤ 5 filler/AI-pattern phrases per 1000 words and ≤ 10% repeated sentences.",
		})
	}

	return core.CheckResult{
		Score:    ratioScore(passing, assessed),
		Findings: findings,
		Evidence: map[string]any{"pages": perPage},
	}, nil
}

// --- content.parasite_markers (findings-only by nature ※ — site-reputation
// abuse) ---

type contentParasiteMarkers struct{}

func (contentParasiteMarkers) ID() core.CheckID          { return "content.parasite_markers" }
func (contentParasiteMarkers) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentParasiteMarkers) Kind() CheckKind           { return KindDeterministic }
func (contentParasiteMarkers) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (contentParasiteMarkers) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)

	var findings []core.Finding
	evidence := map[string]any{}
	var markers []string
	var markerPages []string
	for _, p := range pages {
		if len(p.ParasiteMarkers) == 0 {
			continue
		}
		markers = append(markers, p.ParasiteMarkers...)
		markerPages = append(markerPages, p.URL)
	}
	if len(markerPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "content.parasite_markers",
			Severity:       core.SeverityHigh,
			Title:          "Site-reputation-abuse markers detected",
			Detail:         fmt.Sprintf("Sampled pages carry sections that pattern-match parasite SEO (%v) — third-party commercial content riding the host's authority, which Google's site-reputation-abuse policy targets.", dedupeStrings(markers)),
			Pages:          capPages(markerPages),
			Recommendation: "Remove or clearly separate third-party commercial sections (coupons, casino/loan content, essay services) from the host domain, or noindex them.",
			Falsifiability: "The flagged sections are removed, moved off-domain, or noindexed on re-inspection.",
		})
		evidence["markers"] = dedupeStrings(markers)
		evidence["pages"] = markerPages
	}

	// Intrinsically findings-only: Score nil (spec carries no allocation).
	return core.CheckResult{Score: nil, Findings: findings, Evidence: evidence}, nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
