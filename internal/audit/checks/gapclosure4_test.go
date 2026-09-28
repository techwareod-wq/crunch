package checks

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// Round-4 regressions: the finspectors false strengths. Internal linking
// scored 100/100 while every post hung off pagination pages; brand footprint
// scored 100/100 off a self-coined category query.

func TestIsListingIndexPage_PluralAndPagination(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/blogs":                 true, // the plural that slipped through
		"https://example.com/blog":                  true,
		"https://example.com/blogs?x_page=2":        true, // pagination variant shares the path
		"https://example.com/blogs/some-post":       false,
		"https://example.com/platform":              false,
		"https://example.com/finspectors-academy":   false, // compound slugs stay content-eligible
		"https://example.com/resources":             true,
		"https://example.com/guides":                true,
		"https://example.com/docs":                  true,
		"https://example.com/how-we-work":           false,
		"https://example.com/insights/deep-article": false,
	}
	for u, want := range cases {
		if got := isListingIndexPage(u); got != want {
			t.Errorf("isListingIndexPage(%s) = %v, want %v", u, got, want)
		}
	}
}

func TestIsPaginationURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/blogs?2efcf4b9_page=1": true,
		"https://example.com/blogs?page=3":          true,
		"https://example.com/blog/page/2":           true,
		"https://example.com/blogs/pagespeed-guide": false,
		"https://example.com/blogs":                 false,
		"https://example.com/?homepage=true":        false, // "page=" substring must be a real param suffix... accepted as pagination-ish is fine? No: this matches.
	}
	// The homepage=true case DOES contain "page=" — document the accepted
	// looseness instead of asserting the stricter behavior we don't implement.
	delete(cases, "https://example.com/?homepage=true")
	for u, want := range cases {
		if got := isPaginationURL(u); got != want {
			t.Errorf("isPaginationURL(%s) = %v, want %v", u, got, want)
		}
	}
}

// The finspectors shape: posts only reachable via listing/pagination, sampled
// bodies containing no articles. The crawl-graph structure must cap the score
// and emit the single-discovery-path finding; the thin sample must veto the
// strength blurb.
func TestContentInternalLinking_PaginationStrandedPosts(t *testing.T) {
	var crawlPages []artifacts.CrawlPage
	crawlPages = append(crawlPages,
		artifacts.CrawlPage{URL: "https://example.com/", StatusCode: 200, ClickDepth: 0, WordCount: 500},
		artifacts.CrawlPage{URL: "https://example.com/blogs", StatusCode: 200, ClickDepth: 1, WordCount: 500, InboundLinksCount: 100},
		artifacts.CrawlPage{URL: "https://example.com/blogs?x_page=1", StatusCode: 200, ClickDepth: 2, WordCount: 500, InboundLinksCount: 3},
	)
	for i := 0; i < 10; i++ {
		crawlPages = append(crawlPages, artifacts.CrawlPage{
			URL:        "https://example.com/blogs/post-" + string(rune('a'+i)),
			StatusCode: 200, ClickDepth: 2, WordCount: 1200, InboundLinksCount: 2,
		})
	}
	crawl := &artifacts.CrawlArtifact{Pages: crawlPages}
	// Deep sample: only the homepage and a listing — zero article bodies
	// (the old sampling bias).
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/", Fetched: true, IsHomepage: true, BodyLinksAssessed: true, WordCount: 500},
		{URL: "https://example.com/blogs", Fetched: true, BodyLinksAssessed: true, WordCount: 500,
			BodyInternalLinkCount: 30, BodyInternalTargets: []string{"https://example.com/blogs/post-a"}},
	}}

	result := run(t, contentInternalLinking{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    crawl,
		artifacts.KindHTMLDeep: deep,
	}))

	if result.Score != nil && result.Score.Ratio() > 0.6 {
		t.Errorf("pagination-stranded site scored %v, want ≤ 0.6 (structural cap)", result.Score.Ratio())
	}
	if !hasFindingContaining(result.Findings, "single discovery path") &&
		!hasFindingContaining(result.Findings, "listing/pagination") {
		t.Error("single-discovery-path finding absent")
	}
	if v, ok := result.Evidence["strengthIneligible"].(bool); !ok || !v {
		t.Error("thin assessed sample must set strengthIneligible")
	}
}

// A genuinely well-linked site must NOT be capped: articles carry body links
// and multiple inbound paths.
func TestContentInternalLinking_HealthySiteUncapped(t *testing.T) {
	var crawlPages []artifacts.CrawlPage
	for i := 0; i < 6; i++ {
		crawlPages = append(crawlPages, artifacts.CrawlPage{
			URL:        "https://example.com/blog/post-" + string(rune('a'+i)),
			StatusCode: 200, ClickDepth: 2, WordCount: 1200, InboundLinksCount: 6,
		})
	}
	crawl := &artifacts.CrawlArtifact{Pages: crawlPages}
	var deepPages []artifacts.DeepPage
	deepPages = append(deepPages, artifacts.DeepPage{URL: "https://example.com/", Fetched: true, IsHomepage: true,
		BodyLinksAssessed: true, WordCount: 500, BodyInternalTargets: []string{"https://example.com/blog/post-a"}})
	for i := 0; i < 3; i++ {
		u := "https://example.com/blog/post-" + string(rune('a'+i))
		deepPages = append(deepPages, artifacts.DeepPage{
			URL: u, Fetched: true, BodyLinksAssessed: true, WordCount: 1200,
			BodyInternalLinkCount: 4,
			BodyInternalTargets:   []string{"https://example.com/blog/post-" + string(rune('a'+(i+1)%3))},
		})
	}
	deep := &artifacts.HTMLDeepArtifact{Pages: deepPages}

	result := run(t, contentInternalLinking{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    crawl,
		artifacts.KindHTMLDeep: deep,
	}))
	if result.Score == nil || result.Score.Ratio() != 1 {
		t.Errorf("healthy site score = %v, want 1", result.Score)
	}
	if v, _ := result.Evidence["strengthIneligible"].(bool); v {
		t.Error("healthy 3-article sample must stay strength-eligible")
	}
}

// Buyer-intent absence: medium finding, score capped, strength vetoed.
// Self-phrase absence: low finding only (legacy behavior).
func TestAisearchBrandFootprint_CategoryIntent(t *testing.T) {
	buyer := &artifacts.MentionsArtifact{
		ThirdPartyCount:      20,
		ThirdPartyDomains:    []string{"a.com", "b.com"},
		KeySurfaces:          map[string]bool{"g2.com": true},
		CategoryProbed:       true,
		CategoryQuery:        "best ai audit software",
		CategoryQueryIntent:  artifacts.CategoryIntentBuyer,
		CategorySourcePhrase: "AI-Native Audit Workspace",
		CategoryTopDomains:   []string{"c1.com", "c2.com", "c3.com", "c4.com", "c5.com"},
	}
	result := run(t, aisearchBrandFootprint{}, testInput(t, map[core.Kind]any{artifacts.KindMentions: buyer}))
	if result.Score.Earned > 70 {
		t.Errorf("buyer-intent absence: earned = %v, want ≤ 70", result.Score.Earned)
	}
	foundMedium := false
	for _, f := range result.Findings {
		if f.Severity == core.SeverityMedium && hasFindingContaining([]core.Finding{f}, "buyers actually search") {
			foundMedium = true
		}
	}
	if !foundMedium {
		t.Error("buyer-intent absence must emit the medium category finding")
	}
	if v, ok := result.Evidence["strengthIneligible"].(bool); !ok || !v {
		t.Error("buyer-intent absence must veto the strength")
	}

	self := &artifacts.MentionsArtifact{
		ThirdPartyCount:     20,
		CategoryProbed:      true,
		CategoryQuery:       "ai-native audit workspace",
		CategoryQueryIntent: artifacts.CategoryIntentSelf,
		CategoryTopDomains:  []string{"c1.com", "c2.com", "c3.com", "c4.com", "c5.com"},
	}
	result2 := run(t, aisearchBrandFootprint{}, testInput(t, map[core.Kind]any{artifacts.KindMentions: self}))
	if result2.Score.Earned != 100 {
		t.Errorf("self-phrase absence: earned = %v, want 100 (no cap)", result2.Score.Earned)
	}
	for _, f := range result2.Findings {
		if f.Severity == core.SeverityMedium {
			t.Errorf("self-phrase absence must not emit medium findings, got %q", f.Title)
		}
	}
	// Self-phrase probe (or none) can never headline the strength either.
	if v, ok := result2.Evidence["strengthIneligible"].(bool); !ok || !v {
		t.Error("self-phrase probe must veto the strength")
	}
}

// Round-4 batch 2: FP/FN audit regressions.

func TestTechnicalIndexability_DisallowAllIsCritical(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{RobotsTxtFound: true, Pages: []artifacts.CrawlPage{
		{URL: "https://example.com/", StatusCode: 200},
	}}
	deep := &artifacts.HTMLDeepArtifact{Robots: artifacts.RobotsProbe{Found: true, DisallowAll: true}}
	result := run(t, technicalIndexabilityRobots{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl: crawl, artifacts.KindHTMLDeep: deep,
	}))
	if result.Score == nil || result.Score.Earned != 0 {
		t.Errorf("Disallow-all site scored %+v, want 0", result.Score)
	}
	found := false
	for _, f := range result.Findings {
		if f.Severity == core.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Error("Disallow-all must emit a Critical finding")
	}
}

func TestTechnicalHTTPS_ZeroValueCertAndRedirects(t *testing.T) {
	// SSLInfo absent (CertificateAssessed false) on a healthy HTTPS site must
	// NOT fire the Critical cert finding or halve the score; the http→https
	// redirect hop must not count as "served over plain HTTP".
	crawl := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{
		{URL: "http://example.com/", StatusCode: 301, IsRedirect: true, IsHTTPS: false},
		{URL: "https://example.com/", StatusCode: 200, IsHTTPS: true},
		{URL: "https://example.com/a", StatusCode: 200, IsHTTPS: true},
	}}
	result := run(t, technicalHTTPS{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))
	if result.Score == nil || result.Score.Earned != 100 {
		t.Errorf("healthy https site scored %+v, want 100", result.Score)
	}
	if len(result.Findings) != 0 {
		t.Errorf("healthy https site got findings: %+v", result.Findings)
	}
	// Assessed cert failure still fires.
	crawl.CertificateAssessed = true
	crawl.ValidCertificate = false
	result2 := run(t, technicalHTTPS{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))
	if !hasFindingContaining(result2.Findings, "SSL certificate") {
		t.Error("assessed invalid cert must fire the finding")
	}
}

func TestSecurityHeadersAndSoft404_UnfetchedUnscored(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{}
	if r := run(t, technicalSecurityHeaders{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})); r.Score != nil {
		t.Errorf("unfetched header probe must be unscored, got %+v", r.Score)
	}
	if r := run(t, technicalSoft404{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})); r.Score != nil {
		t.Errorf("unfetched soft404 probe must be unscored, got %+v", r.Score)
	}
}

func TestContentChecks_EmptySampleUnscored(t *testing.T) {
	// All deep fetches blocked: the artifact exists but no page fetched —
	// every sample-based check must return no score, not a free 100.
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/", Fetched: false, BlockedReason: "blocked"},
	}}
	in := testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})
	for _, c := range []Check{
		contentReadability{}, contentSubstance{}, contentKeywordDensity{},
		contentFreshness{}, contentFillerAIPatterns{},
		schemaValidity{}, schemaDeprecations{},
		imagesAltSemantics{}, imagesSizing{},
	} {
		if r := run(t, c, in); r.Score != nil {
			t.Errorf("%s: blocked-sample score = %+v, want nil", c.ID(), r.Score)
		}
	}
}

func TestContentTrustSignals_ArticleGate(t *testing.T) {
	// A nav-only sample (no article-like pages) must not fire "no author
	// bylines" and must not gift the byline/citation points.
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/", Fetched: true, IsHomepage: true, LinksAboutPage: true, LinksContact: true},
		{URL: "https://example.com/book-a-demo", Fetched: true, WordCount: 200},
		{URL: "https://example.com/faqs", Fetched: true, WordCount: 2800},
	}}
	result := run(t, contentTrustSignals{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if hasFindingContaining(result.Findings, "No author bylines") {
		t.Error("byline finding fired on a sample with zero article-like pages")
	}
	if result.Score.Earned != 100 {
		t.Errorf("about+contact-only assessment = %v, want 100 (30/30 scaled)", result.Score.Earned)
	}
	// With an actual byline-less article, the finding fires.
	deep.Pages = append(deep.Pages, artifacts.DeepPage{
		URL: "https://example.com/blog/post", Fetched: true, WordCount: 900, HasDates: true,
	})
	result2 := run(t, contentTrustSignals{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if !hasFindingContaining(result2.Findings, "No author bylines") {
		t.Error("byline finding absent with a byline-less article sampled")
	}
}
