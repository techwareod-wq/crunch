package collectors

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// crawlPageBatch is the on_page/pages page size (paged up to the run's cap).
const crawlPageBatch = 100

// dupContentEnrichCap bounds the per-run on_page/duplicate_content calls —
// enrichment detail for pages the crawl already flagged, never a full sweep.
const dupContentEnrichCap = 5

// crawlCollector normalizes a FINISHED OnPage task into the crawl artifact.
// It is registered like every collector (so Produces() participates in boot
// validation) but executed by the dedicated AUDIT_CRAWL stage, which owns
// task_post + the in-handler poll and calls Collect once
// run.OnPageTaskID's crawl reports finished. Critical: a run without a crawl
// is not a report (§8.3 row 1).
type crawlCollector struct{}

func (crawlCollector) ID() core.CollectorID            { return CollectorCrawl }
func (crawlCollector) Produces() []core.Kind           { return []core.Kind{artifacts.KindCrawl} }
func (crawlCollector) AppliesTo(*models.AuditRun) bool { return true }
func (crawlCollector) Critical() bool                  { return true }

func (c crawlCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	if run.OnPageTaskID == "" {
		return fmt.Errorf("crawl collector: run %s has no OnPage task id", run.ID.Hex())
	}

	summaryResp, err := deps.DFS.GetOnPageSummary(ctx, run.OnPageTaskID)
	if err != nil {
		return fmt.Errorf("crawl collector: summary: %w", err)
	}
	summary, err := onPageSummaryResult(summaryResp)
	if err != nil {
		return fmt.Errorf("crawl collector: %w", err)
	}

	pages, err := c.fetchPages(ctx, deps, run)
	if err != nil {
		return err
	}

	brokenLinks, linksErr := c.fetchBrokenLinks(ctx, deps, run)
	if linksErr != nil {
		// Links detail is enrichment on top of per-page statuses — degrade
		// with a log, don't fail the critical collector for it.
		log.Warn("audit crawl: links fetch failed, continuing without link detail", "runId", run.ID.Hex(), "error", linksErr)
	}

	artifact := &artifacts.CrawlArtifact{
		StartURL:      run.TargetURL,
		PageCap:       run.PageCap,
		BrokenLinks:   brokenLinks,
		LinksAssessed: linksErr == nil,
		Pages:         pages,
	}
	if summary.CrawlStatus != nil {
		artifact.PagesCrawled = summary.CrawlStatus.PagesCrawled
	}
	if summary.PageMetrics != nil {
		artifact.OnPageScore = summary.PageMetrics.OnPageScore
		artifact.InternalLinksTotal = summary.PageMetrics.LinksInternal
		artifact.ExternalLinksTotal = summary.PageMetrics.LinksExternal
	}
	if summary.DomainInfo != nil {
		artifact.SitemapInRobots = summary.DomainInfo.Checks["sitemap"]
		artifact.RobotsTxtFound = summary.DomainInfo.Checks["robots_txt"]
		if summary.DomainInfo.SSLInfo != nil {
			artifact.ValidCertificate = summary.DomainInfo.SSLInfo.ValidCertificate
			artifact.CertificateAssessed = true
		}
	}

	// Duplicate-content enrichment for a bounded sample of flagged pages.
	artifact.DuplicatePages = c.enrichDuplicates(ctx, deps, run, pages)

	// Deep-pass sample selection lives HERE and only here (decision 15):
	// homepage + most-linked/shallowest, sized per run kind.
	artifact.DeepPassSample = selectDeepPassSample(run, pages, deps.Values.Sampling.DeepPassPages, deps.Values.Sampling.LeadDeepPassPages)

	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindCrawl, artifact)
}

func onPageSummaryResult(resp *dto.OnPageSummaryResponse) (*dto.OnPageSummaryResult, error) {
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return nil, fmt.Errorf("summary returned no result")
	}
	return &resp.Tasks[0].Result[0], nil
}

func (crawlCollector) fetchPages(ctx context.Context, deps Deps, run *models.AuditRun) ([]artifacts.CrawlPage, error) {
	var pages []artifacts.CrawlPage
	// The paged on_page/pages endpoint has no ordering guarantee across
	// requests, so an item can straddle two offsets — dedupe by exact URL
	// (first occurrence wins) or the page double-counts in every check.
	seen := map[string]struct{}{}
	for offset := 0; offset < run.PageCap; offset += crawlPageBatch {
		resp, err := deps.DFS.GetOnPagePages(ctx, dto.OnPagePagesRequest{Tasks: []dto.OnPagePagesTask{{
			ID:     run.OnPageTaskID,
			Limit:  crawlPageBatch,
			Offset: offset,
		}}})
		if err != nil {
			return nil, fmt.Errorf("crawl collector: pages offset %d: %w", offset, err)
		}
		if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
			break
		}
		result := resp.Tasks[0].Result[0]
		for _, item := range result.Items {
			if item.ResourceType != "html" {
				continue
			}
			if _, dup := seen[item.URL]; dup {
				continue
			}
			seen[item.URL] = struct{}{}
			pages = append(pages, normalizeCrawlPage(item))
		}
		if len(result.Items) < crawlPageBatch {
			break
		}
	}
	return pages, nil
}

func normalizeCrawlPage(item dto.OnPagePageItem) artifacts.CrawlPage {
	p := artifacts.CrawlPage{
		URL:         item.URL,
		StatusCode:  item.StatusCode,
		ClickDepth:  item.ClickDepth,
		OnPageScore: item.OnPageScore,
	}
	if item.Meta != nil {
		p.Title = item.Meta.Title
		p.MetaDescription = item.Meta.Description
		p.Canonical = item.Meta.Canonical
		p.H1Count = len(item.Meta.HTags["h1"])
		if h1s := item.Meta.HTags["h1"]; len(h1s) > 0 {
			if len(h1s) > 3 {
				h1s = h1s[:3]
			}
			p.H1Texts = h1s
		}
		p.InternalLinksCount = item.Meta.InternalLinksCount
		p.ExternalLinksCount = item.Meta.ExternalLinksCount
		p.InboundLinksCount = item.Meta.InboundLinksCount
		p.ImagesCount = item.Meta.ImagesCount
		if item.Meta.Content != nil {
			p.WordCount = int(item.Meta.Content.PlainTextWordCount)
		}
	}
	if item.PageTiming != nil {
		p.LCPMs = item.PageTiming.LargestContentfulPaint
	}
	checks := item.Checks
	p.IsHTTPS = checks["is_https"]
	p.IsRedirect = checks["is_redirect"]
	p.IsBroken = checks["is_broken"]
	// The OnPage check-name for a meta/x-robots noindex has shifted across
	// API revisions — read every observed key; all absent ⇒ false (degrade
	// to "not assessed", never a false positive).
	p.NoIndex = checks["no_index"] || checks["noindex"] || checks["is_noindex"]
	p.CanonicalChain = checks["canonical_chain"]
	p.CanonicalToRedirect = checks["canonical_to_redirect"]
	p.CanonicalToBroken = checks["canonical_to_broken"]
	p.RecursiveCanonical = checks["recursive_canonical"]
	p.HTTPSToHTTPLinks = checks["https_to_http_links"]
	p.DuplicateContent = item.DuplicateContent
	return p
}

func (crawlCollector) fetchBrokenLinks(ctx context.Context, deps Deps, run *models.AuditRun) ([]artifacts.BrokenLink, error) {
	resp, err := deps.DFS.GetOnPageLinks(ctx, dto.OnPageLinksRequest{Tasks: []dto.OnPageLinksTask{{
		ID:    run.OnPageTaskID,
		Limit: 1000,
	}}})
	if err != nil {
		return nil, err
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return nil, nil
	}
	var out []artifacts.BrokenLink
	for _, l := range resp.Tasks[0].Result[0].Items {
		if l.Direction == "internal" && l.IsBroken {
			out = append(out, artifacts.BrokenLink{FromURL: l.LinkFrom, ToURL: l.LinkTo})
		}
	}
	return out, nil
}

func (crawlCollector) enrichDuplicates(ctx context.Context, deps Deps, run *models.AuditRun, pages []artifacts.CrawlPage) []artifacts.DuplicateContentGroup {
	var out []artifacts.DuplicateContentGroup
	enriched := 0
	for _, p := range pages {
		if !p.DuplicateContent || enriched >= dupContentEnrichCap {
			continue
		}
		enriched++
		resp, err := deps.DFS.GetOnPageDuplicateContent(ctx, dto.OnPageDuplicateContentRequest{Tasks: []dto.OnPageDuplicateContentTask{{
			ID:    run.OnPageTaskID,
			URL:   p.URL,
			Limit: 10,
		}}})
		if err != nil {
			log.Warn("audit crawl: duplicate_content enrich failed", "url", p.URL, "error", err)
			continue
		}
		if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
			continue
		}
		group := artifacts.DuplicateContentGroup{URL: p.URL}
		for _, item := range resp.Tasks[0].Result[0].Items {
			if item.Page != nil && item.Page.URL != "" {
				group.Similar = append(group.Similar, item.Page.URL)
				if item.Similarity > group.Similarity {
					group.Similarity = item.Similarity
				}
			}
		}
		if len(group.Similar) > 0 {
			out = append(out, group)
		}
	}
	return out
}

// templateClusterMinSize: a URL group this large is a CMS collection sharing
// one template, and the deep pass must sample inside it.
const templateClusterMinSize = 3

// selectDeepPassSample picks the deep-pass pages (decision 15). Sample size:
// tenant deepPassPages, lead leadDeepPassPages.
//
// Stratified by template cluster (gap-closure round 4): pure inbound-link
// ranking always selects nav/footer chrome — every page links the nav pages,
// no page links an individual article — so the deep pass never contained a
// single item from the site's CMS collections (/blog/*, /products/*, ...).
// Every per-template defect (broken article JSON-LD, missing bylines, orphaned
// post bodies) was structurally invisible. Now: homepage first, then one
// representative per item cluster (largest collections first), then the
// remaining slots by the legacy inbound/depth rank.
func selectDeepPassSample(run *models.AuditRun, pages []artifacts.CrawlPage, tenantN, leadN int) []string {
	n := tenantN
	if core.RunKind(run.Kind) == core.RunKindLead {
		n = leadN
	}
	if n <= 0 {
		n = 1
	}

	var homepage string
	var candidates []artifacts.CrawlPage
	for _, p := range pages {
		if p.StatusCode != 200 || p.IsRedirect || p.NoIndex {
			continue
		}
		if homepage == "" && (p.ClickDepth == 0 || sameNormalizedURL(p.URL, run.TargetURL)) {
			homepage = p.URL
			continue
		}
		candidates = append(candidates, p)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].InboundLinksCount != candidates[j].InboundLinksCount {
			return candidates[i].InboundLinksCount > candidates[j].InboundLinksCount
		}
		return candidates[i].ClickDepth < candidates[j].ClickDepth
	})

	var sample []string
	taken := map[string]bool{}
	add := func(u string) {
		if u == "" || taken[u] || len(sample) >= n {
			return
		}
		taken[u] = true
		sample = append(sample, u)
	}

	if homepage != "" {
		add(homepage)
	} else if len(pages) > 0 {
		// No page identified itself as the homepage — fall back to the
		// target URL so the deep pass always covers the entry point.
		add(run.TargetURL)
	}

	// One representative per item cluster, largest cluster first — the
	// biggest collection is the site's content engine and its template twin
	// pages are where scaled defects live. The representative is the cluster's
	// most substantive member (word count, then inbound links), so the deep
	// pass judges a real article, not the collection's thinnest stub.
	clusters := map[string][]artifacts.CrawlPage{}
	for _, p := range candidates {
		if key := templateClusterKey(p.URL); key != "" {
			clusters[key] = append(clusters[key], p)
		}
	}
	var clusterKeys []string
	for key, members := range clusters {
		if len(members) >= templateClusterMinSize {
			clusterKeys = append(clusterKeys, key)
		}
	}
	sort.SliceStable(clusterKeys, func(i, j int) bool {
		if len(clusters[clusterKeys[i]]) != len(clusters[clusterKeys[j]]) {
			return len(clusters[clusterKeys[i]]) > len(clusters[clusterKeys[j]])
		}
		return clusterKeys[i] < clusterKeys[j]
	})
	// Reserve slots so cluster reps can never crowd out the top nav pages
	// entirely (half the non-homepage budget, at least one when any cluster
	// exists).
	repBudget := (n - 1) / 2
	if repBudget < 1 && len(clusterKeys) > 0 && n > 1 {
		repBudget = 1
	}
	for _, key := range clusterKeys {
		if repBudget <= 0 {
			break
		}
		members := clusters[key]
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].WordCount != members[j].WordCount {
				return members[i].WordCount > members[j].WordCount
			}
			return members[i].InboundLinksCount > members[j].InboundLinksCount
		})
		add(members[0].URL)
		repBudget--
	}

	for _, p := range candidates {
		if len(sample) >= n {
			break
		}
		add(p.URL)
	}
	return sample
}

// templateClusterKey groups a URL into its template cluster: pages under a
// shared first path segment ("/blogs/foo" → "blogs/*"). Single-segment pages
// (nav/landing) and the root return "" — they are individually-authored, not
// template items. Query-string variants of a listing (pagination) also return
// "" so ?page=N mirrors can't form a fake cluster.
func templateClusterKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || segs[0] == "" {
		return ""
	}
	return strings.ToLower(segs[0]) + "/*"
}

func sameNormalizedURL(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "www.")
		return strings.TrimRight(s, "/")
	}
	return norm(a) == norm(b)
}
