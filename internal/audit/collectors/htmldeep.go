package collectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
)

// htmlDeepCollector runs the deep pass: fetch the crawl-selected page sample
// through the existing rendered-fetch path, extract every HTML-level feature
// in memory (htmlfeatures.go), and probe the site-level files (sitemap,
// robots.txt, llms.txt, IndexNow key). Non-critical: a failure degrades to a
// constraint and the dependent checks skip.
type htmlDeepCollector struct{}

func (htmlDeepCollector) ID() core.CollectorID            { return CollectorHTMLDeep }
func (htmlDeepCollector) Produces() []core.Kind           { return []core.Kind{artifacts.KindHTMLDeep} }
func (htmlDeepCollector) AppliesTo(*models.AuditRun) bool { return true }
func (htmlDeepCollector) Critical() bool                  { return false }

// siteProbeTimeout bounds each site-level probe fetch (tiny text files).
const siteProbeTimeout = 15 * time.Second

// aiCrawlerAgents are the AI user-agent tokens whose robots posture the
// crawler-access check reports.
var aiCrawlerAgents = []string{
	"GPTBot", "OAI-SearchBot", "ClaudeBot", "Claude-Web", "anthropic-ai",
	"PerplexityBot", "Google-Extended", "CCBot", "Bytespider", "cohere-ai",
}

func (htmlDeepCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	crawl, err := loadCrawlArtifact(ctx, run)
	if err != nil {
		return fmt.Errorf("html_deep collector: %w", err)
	}
	artifact := buildHTMLDeepArtifact(ctx, deps, run, crawl.DeepPassSample, 0)
	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindHTMLDeep, artifact)
}

// BuildHTMLDeepScoped runs the deep pass over an EXPLICIT page list — the
// re-check's scoped mode. Fetch + in-memory feature extraction + the site
// file probes, WITHOUT touching the blackboard (the stored run must never
// mutate). A page equal to the run's target URL counts as the homepage.
func BuildHTMLDeepScoped(ctx context.Context, deps Deps, run *models.AuditRun, pages []string) *artifacts.HTMLDeepArtifact {
	return buildHTMLDeepArtifact(ctx, deps, run, pages, -1)
}

// buildHTMLDeepArtifact is the shared deep-pass body. homepageIdx marks which
// index is the homepage (-1 = match by URL against the run target).
func buildHTMLDeepArtifact(ctx context.Context, deps Deps, run *models.AuditRun, pages []string, homepageIdx int) *artifacts.HTMLDeepArtifact {
	fetchVals := utils.DataForSEOFetchValues{
		TaskOKStatusCode: deps.DFSValues.TaskOkStatusCode,
		FetchTimeout:     time.Duration(deps.DFSValues.FetchTimeoutSeconds) * time.Second,
	}

	artifact := &artifacts.HTMLDeepArtifact{}
	for i, pageURL := range pages {
		isHomepage := i == homepageIdx || (homepageIdx < 0 && pageURL == run.TargetURL)
		doc, rendered, err := utils.FetchRenderedWebsiteDoc(ctx, deps.DFS, fetchVals, pageURL)
		if err != nil {
			// Per-page constraint (§8.3): checks run on the pages that
			// fetched; the blocked page is recorded, never guessed.
			reason := "fetch_failed"
			if errors.Is(err, utils.ErrScrapeBlocked) {
				reason = "blocked"
			}
			log.Warn("audit deep pass: page fetch failed", "url", pageURL, "reason", reason, "error", err)
			artifact.Pages = append(artifact.Pages, artifacts.DeepPage{
				URL:           pageURL,
				Fetched:       false,
				BlockedReason: reason,
				IsHomepage:    isHomepage,
			})
			continue
		}
		page := extractDeepPageFeatures(doc, pageURL, isHomepage)
		// Honesty flag: when the rendered path fell back to the direct no-JS
		// fetch, this document was never browser-rendered — rendered-dependent
		// checks must treat the page as not assessed.
		page.RawFallback = !rendered
		// Rendered-vs-raw parity (2.2): one plain no-JS GET per sampled page —
		// what most AI crawlers actually see. Best-effort; a failed fetch is
		// recorded (RawFetchOK false) and the parity check skips the page.
		probeRawParity(ctx, pageURL, &page)
		artifact.Pages = append(artifact.Pages, page)
	}

	probeSiteFiles(ctx, run, artifact)
	finalizeSiteContext(artifact)
	return artifact
}

// probeRawParity fetches the page WITHOUT JavaScript and records what
// survives: word count and H1 presence.
func probeRawParity(ctx context.Context, pageURL string, page *artifacts.DeepPage) {
	client := &http.Client{Timeout: siteProbeTimeout}
	body, err := utils.FetchURLBody(ctx, client, pageURL, 2<<20)
	if err != nil {
		return
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return
	}
	page.RawFetchOK = true
	// The cap MUST match the rendered side's (extractDeepPageFeatures uses
	// 40000): asymmetric caps clamped only the denominator on long pages and
	// inflated the parity ratio past the pass line.
	page.RawWordCount = len(strings.Fields(utils.ExtractArticleBodyText(doc, 40000)))
	if h1 := findFirstElement(doc, "h1"); h1 != nil && strings.TrimSpace(nodeText(h1)) != "" {
		page.RawH1Found = true
	}
	page.RawMetaDescriptionFound = rawMetaDescriptionPresent(doc)
}

// rawMetaDescriptionPresent reports a non-empty meta[name=description] in a
// parsed no-JS document.
func rawMetaDescriptionPresent(doc *html.Node) bool {
	found := false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "meta") {
			if strings.EqualFold(attrValue(n, "name"), "description") && strings.TrimSpace(attrValue(n, "content")) != "" {
				found = true
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

// finalizeSiteContext derives the artifact-level LLM-context fields: the
// DetectedTech union and the earliest-content-date proxy (2.6).
func finalizeSiteContext(artifact *artifacts.HTMLDeepArtifact) {
	techSeen := map[string]bool{}
	earliest := ""
	consider := func(date string) {
		if len(date) < 10 {
			return
		}
		date = date[:10]
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return
		}
		if earliest == "" || date < earliest {
			earliest = date
		}
	}
	for _, p := range artifact.Pages {
		for _, m := range p.TechMarkers {
			if !techSeen[m] {
				techSeen[m] = true
				artifact.DetectedTech = append(artifact.DetectedTech, m)
			}
		}
		consider(p.PublishedDate)
	}
	for _, e := range artifact.Sitemap.Entries {
		consider(e.LastMod)
	}
	sortStrings(artifact.DetectedTech)
	artifact.EarliestContentDate = earliest
}

// loadCrawlArtifact reads the crawl artifact off the blackboard — the deep
// pass consumes the sample the crawl stage selected (sampling logic lives in
// exactly one place).
func loadCrawlArtifact(ctx context.Context, run *models.AuditRun) (*artifacts.CrawlArtifact, error) {
	raw, err := models.LoadAuditArtifacts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	bundle, err := artifacts.BuildBundle(raw)
	if err != nil {
		return nil, err
	}
	crawl, ok := bundle.Crawl()
	if !ok {
		return nil, fmt.Errorf("crawl artifact missing for run %s", run.ID.Hex())
	}
	return crawl, nil
}

// probeSiteFiles fetches the site-level text files. Every probe is
// best-effort against the ALREADY-VALIDATED target host (targetcheck ran at
// start; lead runs re-validated at crawl time).
func probeSiteFiles(ctx context.Context, run *models.AuditRun, artifact *artifacts.HTMLDeepArtifact) {
	client := &http.Client{Timeout: siteProbeTimeout}
	base := "https://" + run.TargetDomain

	// robots.txt first: its Sitemap: declarations are sitemap-discovery
	// candidates (WordPress's default /wp-sitemap.xml is NOT a conventional
	// path — sites got a false "No XML sitemap found" at 0/100 for it).
	var robotsSitemaps []string
	if body, err := utils.FetchURLBody(ctx, client, base+"/robots.txt", 512<<10); err == nil && !looksLikeHTMLBody(body) {
		artifact.Robots = parseRobotsForAI(string(body))
		robotsSitemaps = robotsSitemapURLs(string(body))
	}

	// Sitemap discovery + quality. Entries + per-field counts feed the
	// prorated lastmod scoring, the coverage delta, and the burst signal.
	entries, sitemapSource := utils.FetchSitemapEntriesWithSource(ctx, base, utils.SitemapFetchOptions{
		FetchTimeout:    siteProbeTimeout,
		MaxBytes:        5 << 20,
		MaxURLs:         500,
		ExtraCandidates: robotsSitemaps,
	})
	if len(entries) > 0 {
		artifact.Sitemap.Found = true
		artifact.Sitemap.URL = sitemapSource
		artifact.Sitemap.EntryCount = len(entries)
		for _, e := range entries {
			probeEntry := artifacts.SitemapProbeEntry{URL: e.URL}
			if !e.LastMod.IsZero() {
				artifact.Sitemap.HasLastmod = true
				artifact.Sitemap.LastmodCount++
				probeEntry.LastMod = e.LastMod.Format("2006-01-02")
			}
			if e.HasPriority {
				artifact.Sitemap.PriorityCount++
			}
			if e.HasChangefreq {
				artifact.Sitemap.ChangefreqCount++
			}
			artifact.Sitemap.Entries = append(artifact.Sitemap.Entries, probeEntry)
		}
	}

	// SPA catch-all rewrites serve index.html with a 200 for EVERY path — an
	// HTML body at a text-file location is the rewrite answering, not the
	// file existing (gap-closure round 4: sites were credited with
	// llms.txt/IndexNow/security.txt they never shipped). robots.txt was
	// fetched above, before sitemap discovery.

	// llms.txt: parsed for depth signals, still zero ranking weight
	// (fidelity must).
	if body, err := utils.FetchURLBody(ctx, client, base+"/llms.txt", 256<<10); err == nil && len(strings.TrimSpace(string(body))) > 0 && !looksLikeHTMLBody(body) {
		artifact.LlmsTxtFound = true
		artifact.LlmsTxt = parseLlmsTxt(string(body), run.TargetDomain)
	}
	// The full-content companion — presence only, same zero weight.
	if body, err := utils.FetchURLBody(ctx, client, base+"/llms-full.txt", 64<<10); err == nil && len(strings.TrimSpace(string(body))) > 0 && !looksLikeHTMLBody(body) {
		artifact.LlmsFullTxtFound = true
	}

	// IndexNow: only the conventional key path is detectable from outside.
	if body, err := utils.FetchURLBody(ctx, client, base+"/indexnow.txt", 4<<10); err == nil && len(strings.TrimSpace(string(body))) > 0 && !looksLikeHTMLBody(body) {
		artifact.IndexNow.KeyFileFound = true
	}

	// Homepage response headers (security-headers check).
	artifact.Headers = probeHeaders(ctx, base)

	// Soft-404: a deterministic, collision-proof nonexistent path per run.
	artifact.Soft404 = probeSoft404(ctx, base+"/"+run.ID.Hex()+"-audit-probe")

	// Small conventional files (findings-only).
	artifact.SiteFiles.Probed = true
	for _, path := range []string{"/manifest.json", "/site.webmanifest"} {
		if body, err := utils.FetchURLBody(ctx, client, base+path, 256<<10); err == nil && len(strings.TrimSpace(string(body))) > 0 && !looksLikeHTMLBody(body) {
			artifact.SiteFiles.ManifestFound = true
			break
		}
	}
	if body, err := utils.FetchURLBody(ctx, client, base+"/.well-known/security.txt", 64<<10); err == nil && len(strings.TrimSpace(string(body))) > 0 && !looksLikeHTMLBody(body) {
		artifact.SiteFiles.SecurityTxtFound = true
	}
}

// robotsSitemapURLs extracts the Sitemap: declarations from a robots.txt
// body (absolute URLs per the spec; relative values are skipped).
func robotsSitemapURLs(robots string) []string {
	var out []string
	for _, line := range strings.Split(robots, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "sitemap") {
			continue
		}
		v := strings.TrimSpace(parts[1])
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			out = append(out, v)
		}
	}
	return out
}

// looksLikeHTMLBody flags a response body that is an HTML document — at a
// text-file path that means an SPA catch-all rewrite answered, not the file.
func looksLikeHTMLBody(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<")
}

// llmsTxtLinkRe matches markdown links: [text](url).
var llmsTxtLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// llmsTxtAboutTerms mark links covering the homepage/about/product layer (vs
// a file that only lists blog content).
var llmsTxtAboutTerms = []string{"about", "company", "product", "features", "pricing", "docs", "documentation"}

// parseLlmsTxt extracts the depth signals from a fetched llms.txt body (it's
// markdown — cheap line parsing).
func parseLlmsTxt(body, domain string) artifacts.LlmsTxtProbe {
	probe := artifacts.LlmsTxtProbe{Found: true, Bytes: len(body)}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if h := strings.TrimSpace(strings.TrimLeft(trimmed, "# ")); h != "" && len(probe.SectionHeadings) < 20 {
				probe.SectionHeadings = append(probe.SectionHeadings, h)
			}
		}
		// "> ..." blockquote = the conventional one-line identity summary.
		if strings.HasPrefix(trimmed, ">") && strings.TrimSpace(strings.TrimPrefix(trimmed, ">")) != "" {
			probe.HasSummary = true
		}
		// A bullet stating a plain fact (no URL): quotable without a fetch.
		if (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")) &&
			!strings.Contains(strings.ToLower(trimmed), "http") {
			probe.FactLineCount++
		}
	}
	probe.HasKeyFacts = probe.FactLineCount >= 3
	domainLower := strings.ToLower(domain)
	for _, m := range llmsTxtLinkRe.FindAllStringSubmatch(body, -1) {
		probe.LinkCount++
		text, target := strings.ToLower(m[1]), strings.ToLower(m[2])
		if strings.Contains(target, domainLower) {
			trimmed := strings.TrimRight(target, "/")
			if strings.HasSuffix(trimmed, domainLower) {
				probe.HasHomepageLink = true
			}
		}
		for _, term := range llmsTxtAboutTerms {
			if strings.Contains(text, term) || strings.Contains(target, term) {
				probe.LinksAboutOrProduct = true
				break
			}
		}
	}
	return probe
}

// probeHeaders GETs the homepage (following redirects) and records which
// security headers the final response carries.
func probeHeaders(ctx context.Context, base string) artifacts.HeadersProbe {
	client := &http.Client{Timeout: siteProbeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return artifacts.HeadersProbe{}
	}
	req.Header.Set("User-Agent", siteProbeUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return artifacts.HeadersProbe{}
	}
	defer resp.Body.Close()

	h := resp.Header
	csp := h.Get("Content-Security-Policy")
	return artifacts.HeadersProbe{
		Fetched:             true,
		HSTS:                h.Get("Strict-Transport-Security") != "",
		CSP:                 csp != "",
		XContentTypeOptions: strings.EqualFold(strings.TrimSpace(h.Get("X-Content-Type-Options")), "nosniff"),
		FrameProtection:     h.Get("X-Frame-Options") != "" || strings.Contains(strings.ToLower(csp), "frame-ancestors"),
		ReferrerPolicy:      h.Get("Referrer-Policy") != "",
		PermissionsPolicy:   h.Get("Permissions-Policy") != "",
		CacheControlNoStore: strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-store"),
	}
}

// probeSoft404 GETs a URL that cannot exist and records the initial status
// plus the post-redirect final status.
func probeSoft404(ctx context.Context, target string) artifacts.Soft404Probe {
	noRedirect := &http.Client{
		Timeout: siteProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	initial, ok := probeStatus(ctx, noRedirect, target)
	if !ok {
		return artifacts.Soft404Probe{}
	}
	probe := artifacts.Soft404Probe{Fetched: true, Status: initial, FinalStatus: initial}
	if initial >= 300 && initial < 400 {
		following := &http.Client{Timeout: siteProbeTimeout}
		if final, ok := probeStatus(ctx, following, target); ok {
			probe.FinalStatus = final
		}
	}
	return probe
}

// siteProbeUserAgent mirrors the sitemap fetch posture: default Go agents get
// 403'd by CDNs, which would corrupt every probe signal.
const siteProbeUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func probeStatus(ctx context.Context, client *http.Client, target string) (int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", siteProbeUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	return resp.StatusCode, true
}

// robotsSuspectCommentMarkers flag a robots.txt comment as an internal
// engineering note: source paths, code extensions, tooling/deploy vocabulary,
// bug references. The file is public — such comments disclose the stack and
// its known quirks to anyone who looks.
var robotsSuspectCommentMarkers = []string{
	"src/", ".ts", ".js", ".go", ".py", ".tsx",
	"todo", "fixme", "hack", "workaround", "quirk", "bug", "404'd",
	"deploy", "vercel", "netlify", "webpack", "keep in sync", "sync with",
}

// robotsSuspectCommentCap bounds how many leaky lines the probe carries.
const robotsSuspectCommentCap = 5

// parseRobotsForAI reports, per known AI crawler, whether the site's rules
// ALLOW it. A crawler counts as blocked only on a full "Disallow: /" in its
// most-specific matching group — partial disallows are normal hygiene, not
// an AI block. It also captures comment hygiene (internal-note leakage) and
// whether any AI crawler has explicit rules.
func parseRobotsForAI(robots string) artifacts.RobotsProbe {
	probe := artifacts.RobotsProbe{Found: true, Crawlers: map[string]bool{}}

	type group struct {
		agents      []string
		disallowAll bool
		allowAll    bool
	}
	var groups []group
	var current *group
	sawRuleInGroup := true
	for _, line := range strings.Split(robots, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "#"); i >= 0 {
			if comment := strings.TrimSpace(strings.TrimPrefix(line[i:], "#")); comment != "" {
				probe.CommentLineCount++
				lower := strings.ToLower(comment)
				for _, marker := range robotsSuspectCommentMarkers {
					if strings.Contains(lower, marker) {
						if len(probe.SuspectComments) < robotsSuspectCommentCap {
							probe.SuspectComments = append(probe.SuspectComments, comment)
						}
						break
					}
				}
			}
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch field {
		case "user-agent":
			if current == nil || sawRuleInGroup {
				groups = append(groups, group{})
				current = &groups[len(groups)-1]
				sawRuleInGroup = false
			}
			current.agents = append(current.agents, strings.ToLower(value))
		case "disallow":
			if current != nil {
				sawRuleInGroup = true
				// "/*" is functionally identical to "/" for every major
				// crawler (RFC 9309 wildcard).
				if value == "/" || value == "/*" {
					current.disallowAll = true
				}
			}
		case "allow":
			if current != nil {
				sawRuleInGroup = true
				// "Allow: /" wins the tie against "Disallow: /" for Google
				// and every longest-match implementation — reporting such a
				// site as fully blocked was a false Medium.
				if value == "/" || value == "/*" {
					current.allowAll = true
				}
			}
		case "crawl-delay":
			sawRuleInGroup = true
		case "sitemap":
			probe.SitemapDirectiveCount++
			sawRuleInGroup = true
		case "host":
			probe.HasHostDirective = true
			sawRuleInGroup = true
		}
	}
	for i := range groups {
		if groups[i].allowAll {
			groups[i].disallowAll = false
		}
	}

	for _, g := range groups {
		for _, a := range g.agents {
			if a == "*" {
				continue
			}
			for _, known := range aiCrawlerAgents {
				if strings.Contains(a, strings.ToLower(known)) {
					probe.HasExplicitAIRules = true
				}
			}
		}
	}

	blockedFor := func(agent string) bool {
		agentLower := strings.ToLower(agent)
		// Most-specific group wins; fall back to *.
		var starBlocked, found, blocked bool
		for _, g := range groups {
			for _, a := range g.agents {
				if a == "*" {
					starBlocked = starBlocked || g.disallowAll
				}
				if strings.Contains(agentLower, a) || strings.Contains(a, agentLower) {
					if a != "*" {
						found = true
						blocked = blocked || g.disallowAll
					}
				}
			}
		}
		if found {
			return blocked
		}
		return starBlocked
	}

	allBlocked := true
	for _, agent := range aiCrawlerAgents {
		blocked := blockedFor(agent)
		probe.Crawlers[agent] = !blocked
		if !blocked {
			allBlocked = false
		}
	}
	probe.DisallowAll = allBlocked && blockedFor("googlebot")
	return probe
}
