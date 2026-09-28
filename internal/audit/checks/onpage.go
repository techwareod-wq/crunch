package checks

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// Canonical text-length thresholds (scope §5 fidelity must: the numbers from
// quality-gates.md win over the reference's conflicting copies).
const (
	titleMinChars = 30
	titleMaxChars = 60
	metaMinChars  = 120
	metaMaxChars  = 160
)

// --- onpage.titles ---

type onpageTitles struct{}

func (onpageTitles) ID() core.CheckID          { return "onpage.titles" }
func (onpageTitles) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageTitles) Kind() CheckKind           { return KindDeterministic }
func (onpageTitles) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (onpageTitles) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var missing, short, long []string
	dupGroups := map[string][]string{}
	assessed := 0
	for _, p := range crawl.Pages {
		// Noindexed pages have no SERP snippet to optimize — assessing them
		// against SERP claims is a category error.
		if p.StatusCode != 200 || p.IsRedirect || p.NoIndex {
			continue
		}
		assessed++
		title := strings.TrimSpace(p.Title)
		n := utf8.RuneCountInString(title)
		switch {
		case n == 0:
			missing = append(missing, p.URL)
		case n < titleMinChars:
			short = append(short, p.URL)
		case n > titleMaxChars:
			long = append(long, p.URL)
		}
		if title != "" {
			key := strings.ToLower(title)
			dupGroups[key] = append(dupGroups[key], p.URL)
		}
	}
	dupPages := flattenDupGroups(dupGroups)

	var findings []core.Finding
	addTitleFinding := func(pages []string, sev core.Severity, title, detail, rec string) {
		if len(pages) == 0 {
			return
		}
		findings = append(findings, core.Finding{
			CheckID: "onpage.titles", Severity: sev, Title: title, Detail: detail,
			Pages: capPages(pages), Recommendation: rec,
			Falsifiability: fmt.Sprintf("Every listed page's <title> is unique and %d–%d characters.", titleMinChars, titleMaxChars),
		})
	}
	addTitleFinding(missing, core.SeverityHigh,
		fmt.Sprintf("%d page(s) with no title tag", len(missing)),
		"Pages without a <title> forfeit the single strongest on-page relevance signal.",
		"Write a unique, descriptive title for each page.")
	addTitleFinding(dupPages, core.SeverityMedium,
		fmt.Sprintf("%d page(s) share duplicate titles", len(dupPages)),
		"Identical titles make pages compete with each other and blur relevance.",
		"Differentiate each title around the page's primary topic.")
	addTitleFinding(short, core.SeverityLow,
		fmt.Sprintf("%d page(s) with a too-short title (<%d chars)", len(short), titleMinChars),
		"Very short titles waste SERP real estate and rarely cover the query.",
		"Expand titles toward the 30–60 character band.")
	addTitleFinding(long, core.SeverityLow,
		fmt.Sprintf("%d page(s) with a too-long title (>%d chars)", len(long), titleMaxChars),
		"Titles past ~60 characters truncate in the SERP.",
		"Trim titles into the 30–60 character band, keeping the primary topic first.")

	// A page counts against the score once, however many buckets it lands in
	// (a short duplicated title used to subtract twice — degenerate inputs
	// went negative).
	bad := distinctURLCount(missing, short, long, dupPages)
	return core.CheckResult{Score: ratioScore(assessed-bad, assessed), Findings: findings}, nil
}

// flattenDupGroups returns the pages of every >1-member group in
// deterministic (sorted-key) order — map iteration order must never decide
// which pages a finding names.
func flattenDupGroups(groups map[string][]string) []string {
	var keys []string
	for k, urls := range groups {
		if len(urls) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, groups[k]...)
	}
	return out
}

// distinctURLCount counts unique URLs across the given failure buckets.
func distinctURLCount(buckets ...[]string) int {
	seen := map[string]bool{}
	for _, b := range buckets {
		for _, u := range b {
			seen[u] = true
		}
	}
	return len(seen)
}

// --- onpage.metas ---

type onpageMetas struct{}

func (onpageMetas) ID() core.CheckID          { return "onpage.metas" }
func (onpageMetas) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageMetas) Kind() CheckKind           { return KindDeterministic }
func (onpageMetas) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (onpageMetas) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var missing, offBand []string
	dupGroups := map[string][]string{}
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect || p.NoIndex {
			continue
		}
		assessed++
		desc := strings.TrimSpace(p.MetaDescription)
		n := utf8.RuneCountInString(desc)
		switch {
		case n == 0:
			missing = append(missing, p.URL)
		case n < metaMinChars || n > metaMaxChars:
			offBand = append(offBand, p.URL)
		}
		if desc != "" {
			dupGroups[strings.ToLower(desc)] = append(dupGroups[strings.ToLower(desc)], p.URL)
		}
	}
	dupPages := flattenDupGroups(dupGroups)

	var findings []core.Finding
	if len(missing) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.metas",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) with no meta description", len(missing)),
			Detail:         "Without a meta description the SERP snippet is whatever Google scrapes.",
			Pages:          capPages(missing),
			Recommendation: "Write a 120–160 character description that earns the click.",
			Falsifiability: fmt.Sprintf("Every listed page carries a unique meta description of %d–%d characters.", metaMinChars, metaMaxChars),
		})
	}
	if len(dupPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.metas",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d page(s) share duplicate meta descriptions", len(dupPages)),
			Detail:         "Duplicated descriptions read as boilerplate and get rewritten by Google.",
			Pages:          capPages(dupPages),
			Recommendation: "Differentiate each description around the page's unique value.",
			Falsifiability: "No two indexable pages share the same meta description.",
		})
	}
	if len(offBand) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.metas",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d meta description(s) outside the %d–%d character band", len(offBand), metaMinChars, metaMaxChars),
			Detail:         "Too short wastes the snippet; too long truncates.",
			Pages:          capPages(offBand),
			Recommendation: "Rewrite into the 120–160 character band.",
			Falsifiability: fmt.Sprintf("Every listed description measures %d–%d characters.", metaMinChars, metaMaxChars),
		})
	}

	// JS-only descriptions (findings-only, deep pass read opportunistically):
	// the crawl renders JavaScript, so a client-side-injected description
	// looks present to every count above — while the raw HTML that social
	// scrapers and most AI crawlers read has none. This was exactly how a
	// no-description page shipped through this check unflagged.
	if deep, ok := in.Bundle.HTMLDeep(); ok {
		descByURL := map[string]string{}
		for _, p := range crawl.Pages {
			descByURL[p.URL] = strings.TrimSpace(p.MetaDescription)
		}
		var jsOnly []string
		for _, dp := range deep.Pages {
			if !dp.Fetched || !dp.RawFetchOK || dp.RawFallback || dp.RawMetaDescriptionFound {
				continue
			}
			if descByURL[dp.URL] != "" {
				jsOnly = append(jsOnly, dp.URL)
			}
		}
		if len(jsOnly) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "onpage.metas",
				Severity:       core.SeverityLow,
				Title:          fmt.Sprintf("%d page(s) whose meta description only renders with JavaScript", len(jsOnly)),
				Detail:         "The rendered DOM carries a description but the raw HTML does not — social scrapers, most AI crawlers, and any no-JS consumer see a page with no description at all.",
				Pages:          capPages(jsOnly),
				Recommendation: "Emit the meta description in the server-rendered HTML head, not via client-side injection.",
				Falsifiability: "curl of each listed URL contains a non-empty <meta name=\"description\"> in the response body.",
			})
		}
	}

	bad := distinctURLCount(missing, offBand, dupPages)
	return core.CheckResult{Score: ratioScore(assessed-bad, assessed), Findings: findings}, nil
}

// --- onpage.headings ---

type onpageHeadings struct{}

func (onpageHeadings) ID() core.CheckID          { return "onpage.headings" }
func (onpageHeadings) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageHeadings) Kind() CheckKind           { return KindDeterministic }
func (onpageHeadings) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (onpageHeadings) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var noH1, multiH1 []string
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect || p.NoIndex {
			continue
		}
		assessed++
		switch {
		case p.H1Count == 0:
			noH1 = append(noH1, p.URL)
		case p.H1Count > 1:
			multiH1 = append(multiH1, p.URL)
		}
	}

	// H1 text-extraction sanity (quality uplift 2.1): the deep pass is read
	// OPPORTUNISTICALLY (same pattern as onpage.sxo_mismatch — not in
	// Requires(), so a deep-pass failure never skips the whole check).
	// Animated hero components dump every rotation state into textContent,
	// which is the H1 crawlers and AI extractors actually read.
	var suspectPages []string
	var suspectExamples []string
	var jsOnlyH1 []string
	if deep, ok := in.Bundle.HTMLDeep(); ok {
		for _, dp := range deep.Pages {
			if !dp.Fetched {
				continue
			}
			// A page whose H1 exists only after JavaScript runs is headless
			// for every raw-HTML consumer (most AI crawlers, social scrapers).
			if dp.RawFetchOK && !dp.RawFallback && !dp.RawH1Found && dp.H1Text != "" {
				jsOnlyH1 = append(jsOnlyH1, dp.URL)
			}
			if !dp.H1Suspect {
				continue
			}
			suspectPages = append(suspectPages, dp.URL)
			if len(suspectExamples) < 2 {
				h1 := dp.H1Text
				if len(h1) > 160 {
					h1 = h1[:160] + "…"
				}
				suspectExamples = append(suspectExamples, fmt.Sprintf("%q (%s)", h1, dp.H1SuspectReason))
			}
		}
	}

	var findings []core.Finding
	if len(suspectPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.headings",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) whose H1 extracts as corrupted/concatenated text", len(suspectPages)),
			Detail:         fmt.Sprintf("The H1 text a crawler extracts is garbled — typically an animated/rotating heading dumping every state into the DOM. Extracted: %s.", strings.Join(suspectExamples, "; ")),
			Pages:          capPages(suspectPages),
			Recommendation: "Keep one static, complete H1 in the DOM; render animation states in aria-hidden elements and put the canonical text in an sr-only (or the static) node so extraction stays clean.",
			Falsifiability: "Fetching each listed page and reading document.querySelector('h1').textContent yields one clean, human-readable heading.",
		})
	}
	if len(noH1) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.headings",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) with no H1", len(noH1)),
			Detail:         "A missing H1 removes the page's structural topic anchor for crawlers and AI extractors alike.",
			Pages:          capPages(noH1),
			Recommendation: "Give every page exactly one H1 stating its topic.",
			Falsifiability: "Every listed page renders exactly one <h1>.",
		})
	}
	if len(multiH1) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.headings",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d page(s) with multiple H1s", len(multiH1)),
			Detail:         "Multiple H1s dilute the topical signal.",
			Pages:          capPages(multiH1),
			Recommendation: "Demote extra H1s to H2s.",
			Falsifiability: "Every listed page renders exactly one <h1>.",
		})
	}

	if len(jsOnlyH1) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.headings",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d page(s) whose H1 only renders with JavaScript", len(jsOnlyH1)),
			Detail:         "A plain no-JS fetch of these pages finds no H1 — raw-HTML consumers (most AI crawlers, social scrapers, some search pipelines) see a heading-less page.",
			Pages:          capPages(jsOnlyH1),
			Recommendation: "Server-render (or statically include) the H1 so it exists in the initial HTML.",
			Falsifiability: "curl of each listed URL contains a non-empty <h1> in the response body.",
		})
	}

	// Cross-page duplicate H1s (the two-product-pages-sharing-one-H1 case).
	h1Groups := map[string][]string{}
	for _, p := range crawl.Pages {
		// Pagination variants legitimately mirror their listing's H1.
		if p.StatusCode != 200 || p.IsRedirect || p.NoIndex || isPaginationURL(p.URL) {
			continue
		}
		for _, t := range p.H1Texts {
			t = strings.ToLower(strings.TrimSpace(t))
			if t != "" {
				h1Groups[t] = append(h1Groups[t], p.URL)
				break // group by the page's first H1
			}
		}
	}
	if dupH1 := flattenDupGroups(h1Groups); len(dupH1) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.headings",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) share an identical H1 with another page", len(dupH1)),
			Detail:         "Different pages carrying the byte-identical H1 tell search engines they cover the same topic — they compete with each other and neither targets its own keyword.",
			Pages:          capPages(dupH1),
			Recommendation: "Give each page a distinct H1 naming its own topic/keyword.",
			Falsifiability: "No two indexable pages render the same <h1> text.",
		})
	}

	// The deep-sample signals (suspect H1s, JS-only H1s, cross-page dups) are
	// findings-only: mixing the 5-page deep sample into the full-crawl
	// denominator both understated real prevalence and could push the ratio
	// negative.
	bad := distinctURLCount(noH1, multiH1)
	return core.CheckResult{Score: ratioScore(assessed-bad, assessed), Findings: findings}, nil
}

// serpConsensusMinResults / serpConsensusStrongShare gate the page-type
// consensus signal: fewer than 5 organic results proves nothing, and below a
// 60% dominant share the SERP is mixed — no expectation to violate.
const (
	serpConsensusMinResults  = 5
	serpConsensusStrongShare = 0.6
)

// listicleTitleRe marks "7 Best X" / "Top 10 Y" shaped titles.
var listicleTitleRe = regexp.MustCompile(`(?i)^\d+\s|\btop\s+\d+\b|\b\d+\s+best\b|\bbest\s+\d+\b`)

// classifySERPPageType buckets a search result (or our own page) into a
// coarse page type from its title and URL — the granularity a consensus
// comparison needs, nothing finer.
func classifySERPPageType(title, rawURL string) string {
	t := strings.ToLower(title)
	path := ""
	if u, err := url.Parse(rawURL); err == nil {
		path = strings.ToLower(strings.Trim(u.Path, "/"))
	}
	switch {
	case strings.HasPrefix(t, "how to ") || strings.Contains(t, " how to "):
		return "how-to"
	case listicleTitleRe.MatchString(t):
		return "listicle"
	case strings.Contains(t, " vs ") || strings.Contains(t, " vs. ") ||
		strings.Contains(t, "alternative") || strings.Contains(t, "comparison") || strings.Contains(t, "compared"):
		return "comparison"
	case strings.Contains(t, "calculator") || strings.Contains(t, "generator") ||
		strings.Contains(t, "checker") || strings.Contains(t, "template") ||
		strings.Contains(path, "tools/") || strings.HasSuffix(path, "tool"):
		return "tool"
	}
	for _, seg := range []string{"blog", "articles", "news", "guide", "guides", "resources", "learn", "posts"} {
		if strings.HasPrefix(path, seg+"/") || strings.Contains(path, "/"+seg+"/") {
			return "article"
		}
	}
	if strings.Count(path, "/") == 0 { // root or one shallow segment
		return "landing"
	}
	return "article"
}

// crossesIntentBoundary reports whether two page types sit on opposite sides
// of the informational↔transactional split — the mismatch class where a
// content page competes against product surfaces (or vice versa).
func crossesIntentBoundary(a, b string) bool {
	transactional := map[string]bool{"landing": true, "tool": true}
	return transactional[a] != transactional[b]
}

// --- onpage.duplicate_content ---

type onpageDuplicateContent struct{}

func (onpageDuplicateContent) ID() core.CheckID          { return "onpage.duplicate_content" }
func (onpageDuplicateContent) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageDuplicateContent) Kind() CheckKind           { return KindDeterministic }
func (onpageDuplicateContent) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (onpageDuplicateContent) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	var dupPages []string
	assessed := 0
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect {
			continue
		}
		assessed++
		if p.DuplicateContent {
			dupPages = append(dupPages, p.URL)
		}
	}

	var findings []core.Finding
	if len(dupPages) > 0 {
		detail := fmt.Sprintf("The crawl flagged %d page(s) as near-duplicates of other pages on the site.", len(dupPages))
		if len(crawl.DuplicatePages) > 0 {
			detail += fmt.Sprintf(" Example: %s closely matches %d other page(s).", crawl.DuplicatePages[0].URL, len(crawl.DuplicatePages[0].Similar))
		}
		findings = append(findings, core.Finding{
			CheckID:        "onpage.duplicate_content",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d near-duplicate page(s)", len(dupPages)),
			Detail:         detail,
			Pages:          capPages(dupPages),
			Recommendation: "Consolidate duplicates (merge + 301) or differentiate their content; canonicalize deliberate variants.",
			Falsifiability: "A re-crawl reports zero duplicate_content flags among indexable pages.",
		})
	}

	return core.CheckResult{Score: ratioScore(assessed-len(dupPages), assessed), Findings: findings}, nil
}

// --- onpage.sxo_mismatch (decision 11: SERP item type vs page format;
// zero new provider methods) ---

type onpageSXOMismatch struct{}

func (onpageSXOMismatch) ID() core.CheckID          { return "onpage.sxo_mismatch" }
func (onpageSXOMismatch) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageSXOMismatch) Kind() CheckKind           { return KindDeterministic }
func (onpageSXOMismatch) Requires() []core.Kind {
	return []core.Kind{artifacts.KindCrawl, artifacts.KindSERP}
}

func (onpageSXOMismatch) Run(_ context.Context, in Input) (core.CheckResult, error) {
	serp, _ := in.Bundle.SERP()
	// The deep pass is optional extra signal here (Requires stays minimal);
	// pages without deep features are judged on SERP presence alone.
	deepByURL := map[string]*artifacts.DeepPage{}
	if deep, ok := in.Bundle.HTMLDeep(); ok {
		for i := range deep.Pages {
			// Blocked pages carry all-zero feature counts — mapping them in
			// would score verified absences off pages never seen.
			if !deep.Pages[i].Fetched {
				continue
			}
			deepByURL[deep.Pages[i].URL] = &deep.Pages[i]
		}
	}

	if len(serp.Pages) == 0 {
		// Nothing ranks → there is no SERP experience to assess. Not a
		// mismatch, but not format health either — unscored.
		return core.CheckResult{Score: nil}, nil
	}

	matched, total := 0, 0
	var findings []core.Finding
	for _, sp := range serp.Pages {
		has := func(t string) bool {
			for _, it := range sp.ItemTypes {
				if it == t {
					return true
				}
			}
			return false
		}
		// Without a fetched deep page there is nothing to verify against —
		// counting an unassessed page as a format HIT scored absence of
		// evidence as health (house-rule inversion).
		dp := deepByURL[sp.URL]
		if dp == nil {
			continue
		}

		var gaps []string
		checksRun := 0
		hit := 0
		if has("featured_snippet") {
			checksRun++
			if dp.AnswerBlockCount > 0 {
				hit++
			} else {
				gaps = append(gaps, "the SERP shows a featured snippet but the page has no extractable answer block")
			}
		}
		if has("people_also_ask") {
			checksRun++
			if dp.QuestionHeadings > 0 {
				hit++
			} else {
				gap := "People-Also-Ask boxes appear but the page uses no question-form headings"
				if len(sp.PAAQuestions) > 0 {
					gap = fmt.Sprintf("People-Also-Ask boxes appear (searchers ask e.g. %q) but the page uses no question-form headings", sp.PAAQuestions[0])
				}
				gaps = append(gaps, gap)
			}
		}
		if has("video") {
			checksRun++
			if dp.VideoEmbedCount > 0 {
				hit++
			} else {
				gaps = append(gaps, "video results rank for this query but the page embeds no video")
			}
		}
		if checksRun == 0 {
			// A plain organic SERP — no format expectation to violate.
			continue
		}
		total += checksRun
		matched += hit
		if len(gaps) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "onpage.sxo_mismatch",
				Severity:       core.SeverityMedium,
				Title:          fmt.Sprintf("Format mismatch vs the SERP for %q", sp.Keyword),
				Detail:         fmt.Sprintf("%s (ranking #%d): %s.", sp.URL, sp.Position, strings.Join(gaps, "; ")),
				Pages:          []string{sp.URL},
				Recommendation: "Match the winning SERP format: add the missing answer block, question headings, or video for this query.",
				Falsifiability: "After the change, the page contains the format element each listed gap names.",
			})
		}
	}

	// Page-type consensus (findings-only, zero score weight — the scored
	// contract above stays unchanged): when the top organic results agree on
	// a page type and our page is a different type, no on-page polish closes
	// that gap — the page is the wrong artifact for the query.
	evidence := map[string]any{}
	consensus := map[string]any{}
	paaByKeyword := map[string][]string{}
	for _, sp := range serp.Pages {
		if len(sp.PAAQuestions) > 0 {
			paaByKeyword[sp.Keyword] = sp.PAAQuestions
		}
		if len(sp.TopResults) < serpConsensusMinResults {
			continue
		}
		counts := map[string]int{}
		typeExample := map[string]string{}
		for _, r := range sp.TopResults {
			t := classifySERPPageType(r.Title, r.URL)
			counts[t]++
			if typeExample[t] == "" {
				typeExample[t] = r.Domain
			}
		}
		domType, domCount := "", 0
		var typeKeys []string
		for t := range counts {
			typeKeys = append(typeKeys, t)
		}
		sort.Strings(typeKeys) // deterministic tie-break, never map order
		for _, t := range typeKeys {
			if counts[t] > domCount {
				domType, domCount = t, counts[t]
			}
		}
		share := float64(domCount) / float64(len(sp.TopResults))
		consensus[sp.Keyword] = map[string]any{"dominantType": domType, "share": share}
		if share < serpConsensusStrongShare {
			continue // mixed/fragmented SERP — no expectation to violate
		}
		// Classifying our page by URL shape alone (no fetched title) fired
		// High "reads as a landing page" claims on pages never inspected —
		// fall back to the crawl title, and skip when neither exists.
		ourTitle := ""
		if dp := deepByURL[sp.URL]; dp != nil {
			ourTitle = dp.Title
		}
		if ourTitle == "" {
			for _, cp := range crawlPagesForSXO(in) {
				if cp.URL == sp.URL {
					ourTitle = cp.Title
					break
				}
			}
		}
		if ourTitle == "" {
			continue
		}
		ourType := classifySERPPageType(ourTitle, sp.URL)
		if ourType == domType {
			continue
		}
		sev := core.SeverityMedium
		if crossesIntentBoundary(ourType, domType) {
			sev = core.SeverityHigh
		}
		findings = append(findings, core.Finding{
			CheckID:  "onpage.sxo_mismatch",
			Severity: sev,
			Title:    fmt.Sprintf("Page type doesn't match what ranks for %q", sp.Keyword),
			Detail: fmt.Sprintf("%d of %d top results are %s pages (e.g. %s), but %s reads as a %s page. When the SERP agrees this strongly, the ranking artifact type is the expectation — a different type competes at a structural disadvantage.",
				domCount, len(sp.TopResults), domType, typeExample[domType], sp.URL, ourType),
			Pages:          []string{sp.URL},
			Recommendation: fmt.Sprintf("Serve the query with a dedicated %s page (keep this page for its own intent), or rework this page into the dominant format.", domType),
			Falsifiability: fmt.Sprintf("A page of type %q targets this keyword, or a re-run shows the SERP consensus changed.", domType),
		})
	}
	if len(consensus) > 0 {
		evidence["serpConsensus"] = consensus
	}
	if len(paaByKeyword) > 0 {
		evidence["paaQuestions"] = paaByKeyword
	}

	if total == 0 {
		// Ranked pages existed but none could be format-verified — unscored.
		return core.CheckResult{Score: nil, Findings: findings, Evidence: evidence}, nil
	}
	return core.CheckResult{Score: ratioScore(matched, total), Findings: findings, Evidence: evidence}, nil
}

// crawlPagesForSXO reads the crawl pages opportunistically (crawl is not in
// this check's Requires — a missing artifact just skips the title fallback).
func crawlPagesForSXO(in Input) []artifacts.CrawlPage {
	crawl, ok := in.Bundle.Crawl()
	if !ok {
		return nil
	}
	return crawl.Pages
}

// --- onpage.programmatic_gates (decision 11: templated/doorway thresholds
// over the crawl's duplicate-content data) ---

type onpageProgrammaticGates struct{}

func (onpageProgrammaticGates) ID() core.CheckID          { return "onpage.programmatic_gates" }
func (onpageProgrammaticGates) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageProgrammaticGates) Kind() CheckKind           { return KindDeterministic }
func (onpageProgrammaticGates) Requires() []core.Kind     { return []core.Kind{artifacts.KindCrawl} }

func (onpageProgrammaticGates) Run(_ context.Context, in Input) (core.CheckResult, error) {
	crawl, _ := in.Bundle.Crawl()

	assessed, thin, dup := 0, 0, 0
	var thinPages []string
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect {
			continue
		}
		assessed++
		if p.DuplicateContent {
			dup++
		}
		if p.WordCount > 0 && p.WordCount < 150 {
			thin++
			thinPages = append(thinPages, p.URL)
		}
	}
	if assessed == 0 {
		return core.CheckResult{Score: nil}, nil
	}

	dupShare := float64(dup) / float64(assessed)
	thinShare := float64(thin) / float64(assessed)

	var findings []core.Finding
	// Doorway gate: a large share of near-identical pages is the programmatic
	// footprint search engines penalize.
	if dupShare > 0.30 && assessed >= 10 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.programmatic_gates",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%.0f%% of crawled pages are near-duplicates of each other", dupShare*100),
			Detail:         "This footprint reads as templated/doorway generation — pages varying only a token while sharing the body.",
			Recommendation: "Add genuinely unique value per page (data, examples, media) or consolidate the template variants.",
			Falsifiability: "A re-crawl reports the duplicate-content share below 30%.",
		})
	}
	if thinShare > 0.30 && assessed >= 10 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.programmatic_gates",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%.0f%% of crawled pages are thin (<150 words)", thinShare*100),
			Detail:         "A site dominated by thin pages fails the programmatic-SEO quality gate.",
			Pages:          capPages(thinPages),
			Recommendation: "Expand or noindex thin template pages.",
			Falsifiability: "The share of sub-150-word indexable pages falls below 30% on a re-crawl.",
		})
	}

	// Duplication weighs heavier than thinness (60/40) — it is the stronger
	// doorway signal. Below the 30%/10-page finding thresholds, this is a
	// gate check, not a proportional-quality check: a couple of legitimately
	// similar pages (legal boilerplate, small sites) must not silently bleed
	// score with no finding explaining it.
	earned := 100.0
	if len(findings) > 0 {
		earned = 100 - (dupShare*60 + thinShare*40)
	}
	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}
