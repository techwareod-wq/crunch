package collectors

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
)

// fixturePage exercises most extraction paths in one document. The answer
// block is exactly 140 words (in the 134–167 citability band).
var fixturePage = `<!DOCTYPE html>
<html><head>
<title>How to Audit a Site</title>
<meta name="author" content="Jane Dev">
<meta property="article:published_time" content="2026-05-01T10:00:00Z">
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Organization","name":"Example","sameAs":["https://linkedin.com/company/example","https://x.com/example"]}
</script>
<script type="application/ld+json">
{"@type":"FAQPage","mainEntity":[]}
</script>
<script type="application/ld+json">not even json</script>
<script type="speculationrules">{"prerender":[{"where":{"href_matches":"/*"}}]}</script>
</head>
<body>
<nav><a href="/about-us">About</a><a href="/contact">Contact</a></nav>
<main><article>
<h1>How to Audit a Site</h1>
<h2>What is an SEO audit?</h2>
<p>` + strings.TrimSpace(strings.Repeat("word ", 140)) + `</p>
<h2>Steps</h2>
<ul><li>one</li><li>two</li></ul>
<table><tr><td>x</td></tr></table>
<img src="/img/hero.png" alt="A dashboard screenshot" width="1600" height="900">
<img src="/img/thumb.png" width="200" height="100">
<img src="/img/huge.png" alt="huge" width="2400" height="1200">
<iframe src="https://www.youtube.com/embed/abc"></iframe>
<a href="https://external.example.org/source">a source</a>
<a href="/internal">internal</a>
</article></main>
</body></html>`

func parseFixture(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}

func TestExtractDeepPageFeatures(t *testing.T) {
	page := extractDeepPageFeatures(parseFixture(t, fixturePage), "https://example.com/how-to-audit", false)

	if page.Title != "How to Audit a Site" {
		t.Errorf("title = %q", page.Title)
	}
	if page.AnswerBlockCount != 1 {
		t.Errorf("answer blocks = %d, want 1 (the 140-word paragraph is in the 134–167 band)", page.AnswerBlockCount)
	}
	if page.QuestionHeadings < 2 {
		t.Errorf("question headings = %d, want ≥2 (\"How to…\" + \"What is…?\")", page.QuestionHeadings)
	}
	if !page.HasMain || !page.HasArticleTag || !page.HasNav {
		t.Errorf("landmarks: main=%v article=%v nav=%v, want all true", page.HasMain, page.HasArticleTag, page.HasNav)
	}
	if !page.HasByline || page.AuthorName != "Jane Dev" {
		t.Errorf("byline=%v author=%q, want Jane Dev", page.HasByline, page.AuthorName)
	}
	if !page.HasDates || page.PublishedDate != "2026-05-01" {
		t.Errorf("dates=%v published=%q", page.HasDates, page.PublishedDate)
	}
	if !page.LinksAboutPage || !page.LinksContact {
		t.Error("about/contact links not detected")
	}
	if !page.HasOrganizationSchema || page.OrganizationSameAsCount != 2 {
		t.Errorf("org schema=%v sameAs=%d, want true/2", page.HasOrganizationSchema, page.OrganizationSameAsCount)
	}
	if len(page.SchemaBlocks) != 3 {
		t.Fatalf("schema blocks = %d, want 3", len(page.SchemaBlocks))
	}
	invalid := 0
	for _, b := range page.SchemaBlocks {
		if !b.Valid {
			invalid++
		}
	}
	if invalid != 1 {
		t.Errorf("invalid schema blocks = %d, want exactly the garbage one", invalid)
	}
	if !page.HasSpeculationRules {
		t.Error("speculationrules script not detected")
	}
	if page.VideoEmbedCount != 1 {
		t.Errorf("video embeds = %d, want 1", page.VideoEmbedCount)
	}
	if page.ListCount != 1 || page.TableCount != 1 {
		t.Errorf("lists=%d tables=%d, want 1/1", page.ListCount, page.TableCount)
	}
	if page.ExternalLinkCount != 1 {
		t.Errorf("external links = %d, want 1", page.ExternalLinkCount)
	}

	// Images: alt + the 3-tier sizing.
	if len(page.Images) != 3 {
		t.Fatalf("images = %d, want 3", len(page.Images))
	}
	byWidth := map[int]bool{}
	for _, img := range page.Images {
		byWidth[img.Width] = true
		switch img.Width {
		case 200:
			if img.Tier != "thumbnail" || img.HasAlt {
				t.Errorf("thumb image: tier=%q hasAlt=%v", img.Tier, img.HasAlt)
			}
		case 1600:
			if img.Tier != "hero" || img.Oversized {
				t.Errorf("hero image: tier=%q oversized=%v", img.Tier, img.Oversized)
			}
		case 2400:
			if !img.Oversized {
				t.Error("2400px image must flag oversized (hero ceiling 2000)")
			}
		}
	}
	if len(byWidth) != 3 {
		t.Errorf("image widths = %v", byWidth)
	}
}

// TestExtractBodyLinkFeatures_Basics: the fixture's article body carries one
// internal link and one real external citation.
func TestExtractBodyLinkFeatures_Basics(t *testing.T) {
	page := extractDeepPageFeatures(parseFixture(t, fixturePage), "https://example.com/how-to-audit", false)
	if !page.BodyLinksAssessed {
		t.Fatal("BodyLinksAssessed must be true after extraction")
	}
	if page.BodyInternalLinkCount != 1 {
		t.Errorf("body internal links = %d, want 1 (nav links are chrome, not body)", page.BodyInternalLinkCount)
	}
	if page.BodyCitationCount != 1 || len(page.BodyCitationDomains) != 1 || page.BodyCitationDomains[0] != "external.example.org" {
		t.Errorf("citations = %d domains = %v, want 1/[external.example.org]", page.BodyCitationCount, page.BodyCitationDomains)
	}
}

// TestExtractBodyLinkFeatures_SocialCTAsAreNotCitations is the tryelip
// regression (quality uplift 1.2): a page whose only external links are
// wa.me/social CTAs must yield BodyCitationCount == 0.
func TestExtractBodyLinkFeatures_SocialCTAsAreNotCitations(t *testing.T) {
	src := `<html><body><main><article>
<h1>A Post</h1><p>Some content here.</p>
<a href="https://wa.me/1234567890">Chat on WhatsApp</a>
<a href="https://api.whatsapp.com/send?phone=1">CTA</a>
<a href="https://www.instagram.com/example">Insta</a>
<a href="https://x.com/example">X</a>
<a href="https://www.example.com/self">absolute self-link</a>
</article></main>
<footer><a href="https://facebook.com/example">FB</a></footer>
</body></html>`
	page := extractDeepPageFeatures(parseFixture(t, src), "https://example.com/post", false)
	if page.BodyCitationCount != 0 {
		t.Errorf("BodyCitationCount = %d, want 0 — CTA/social links are not citations (domains: %v)",
			page.BodyCitationCount, page.BodyCitationDomains)
	}
	if page.BodyExternalLinkCount != 4 {
		t.Errorf("BodyExternalLinkCount = %d, want 4 (footer link excluded, self-link internal)", page.BodyExternalLinkCount)
	}
	if page.BodyInternalLinkCount != 1 {
		t.Errorf("BodyInternalLinkCount = %d, want 1 (the absolute self-link)", page.BodyInternalLinkCount)
	}
}

// TestDetectH1Corruption is the tryelip regression (quality uplift 2.1): an
// H1 of concatenated animation states must set H1Suspect.
func TestDetectH1Corruption(t *testing.T) {
	src := `<html><body><main>
<h1>Meet Elip, yourjob agent in WhatsAppiMessagesoonWhatsAppTelegramsoonWhatsApp</h1>
<p>content</p></main></body></html>`
	page := extractDeepPageFeatures(parseFixture(t, src), "https://example.com/", true)
	if !page.H1Suspect {
		t.Fatalf("H1Suspect = false for concatenated animation states (h1=%q)", page.H1Text)
	}
	if page.H1SuspectReason == "" {
		t.Error("H1SuspectReason must name the trigger")
	}

	clean := extractDeepPageFeatures(parseFixture(t, `<html><body><main><h1>How to Audit a Site Properly</h1></main></body></html>`), "https://example.com/", true)
	if clean.H1Suspect {
		t.Errorf("clean H1 flagged suspect: %q (%s)", clean.H1Text, clean.H1SuspectReason)
	}
}

// TestHeadHygieneAndTechDetection: og/twitter/lang capture + stack markers.
func TestHeadHygieneAndTechDetection(t *testing.T) {
	src := `<html lang="en"><head>
<meta property="og:title" content="T"><meta property="og:image" content="https://example.com/i.png">
<meta name="twitter:card" content="summary_large_image">
<meta name="generator" content="WordPress 6.5">
<script src="/_next/static/chunks/main.js"></script>
</head><body><main><p>x</p></main></body></html>`
	page := extractDeepPageFeatures(parseFixture(t, src), "https://example.com/", true)
	if !page.HasOGTitle || !page.HasOGImage || !page.HasTwitterCard {
		t.Errorf("social meta: og:title=%v og:image=%v twitter:card=%v", page.HasOGTitle, page.HasOGImage, page.HasTwitterCard)
	}
	if page.PageLang != "en" {
		t.Errorf("PageLang = %q, want en", page.PageLang)
	}
	markers := map[string]bool{}
	for _, m := range page.TechMarkers {
		markers[m] = true
	}
	if !markers["WordPress"] || !markers["Next.js"] {
		t.Errorf("tech markers = %v, want WordPress + Next.js", page.TechMarkers)
	}
}

func TestParseLlmsTxt(t *testing.T) {
	body := "# Example\n\n## Blog\n- [Post one](https://example.com/blog/one)\n- [Post two](https://example.com/blog/two)\n"
	probe := parseLlmsTxt(body, "example.com")
	if probe.LinkCount != 2 || probe.HasHomepageLink || probe.LinksAboutOrProduct {
		t.Errorf("content-only llms.txt: links=%d homepage=%v about=%v", probe.LinkCount, probe.HasHomepageLink, probe.LinksAboutOrProduct)
	}
	full := parseLlmsTxt("# Example\n- [Home](https://example.com/)\n- [About us](https://example.com/about)\n", "example.com")
	if !full.HasHomepageLink || !full.LinksAboutOrProduct {
		t.Errorf("full-coverage llms.txt: homepage=%v about=%v, want true/true", full.HasHomepageLink, full.LinksAboutOrProduct)
	}
	// A link index states no facts: bullets carrying URLs don't count.
	if full.HasSummary || full.HasKeyFacts || full.FactLineCount != 0 {
		t.Errorf("link-index llms.txt: summary=%v keyFacts=%v factLines=%d, want false/false/0", full.HasSummary, full.HasKeyFacts, full.FactLineCount)
	}
	facts := parseLlmsTxt("# Example\n\n> Example is a widget service.\n\n## Key facts\n- Free for makers; buyers pay per order.\n- Operated by Example Pvt Ltd, Pune, India.\n- Primary market: India.\n", "example.com")
	if !facts.HasSummary || !facts.HasKeyFacts || facts.FactLineCount != 3 {
		t.Errorf("key-facts llms.txt: summary=%v keyFacts=%v factLines=%d, want true/true/3", facts.HasSummary, facts.HasKeyFacts, facts.FactLineCount)
	}
}

func TestRootDomain(t *testing.T) {
	for host, want := range map[string]string{
		"www.reddit.com":       "reddit.com",
		"old.reddit.com":       "reddit.com",
		"news.ycombinator.com": "ycombinator.com",
		"example.co.uk":        "example.co.uk",
		"blog.example.co.uk":   "example.co.uk",
		"tryelip.ai":           "tryelip.ai",
	} {
		if got := rootDomain(host); got != want {
			t.Errorf("rootDomain(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestDetectParasiteMarkers(t *testing.T) {
	markers := detectParasiteMarkers("https://example.com/best-casino-bonus", nil)
	if len(markers) != 1 || markers[0] != "path:casino" {
		t.Errorf("markers = %v, want [path:casino]", markers)
	}
	if got := detectParasiteMarkers("https://example.com/blog/seo-tips", nil); len(got) != 0 {
		t.Errorf("clean path produced markers: %v", got)
	}
}

func TestParseRobotsForAI(t *testing.T) {
	robots := `
User-agent: *
Disallow: /admin

User-agent: GPTBot
Disallow: /

User-agent: ClaudeBot
Allow: /
`
	probe := parseRobotsForAI(robots)
	if probe.Crawlers["GPTBot"] {
		t.Error("GPTBot must be blocked (Disallow: /)")
	}
	if !probe.Crawlers["ClaudeBot"] {
		t.Error("ClaudeBot must be allowed")
	}
	// The * group only disallows /admin — not a full block.
	if !probe.Crawlers["PerplexityBot"] {
		t.Error("PerplexityBot falls back to * which is not a full block")
	}
	// GPTBot/ClaudeBot have their own groups → explicit AI rules present.
	if !probe.HasExplicitAIRules {
		t.Error("explicit AI groups present but HasExplicitAIRules is false")
	}
}

func TestParseRobotsForAI_CommentHygiene(t *testing.T) {
	leaky := parseRobotsForAI(`# Static file served by Vercel; app/robots.ts 404'd in prod (route-group quirk).
# Keep the Sitemap line in sync with SITE_URL in src/lib/site.ts.
User-agent: *
Allow: /
`)
	if leaky.CommentLineCount != 2 {
		t.Errorf("comment count = %d, want 2", leaky.CommentLineCount)
	}
	if len(leaky.SuspectComments) != 2 {
		t.Errorf("suspect comments = %v, want both lines flagged", leaky.SuspectComments)
	}
	if leaky.HasExplicitAIRules {
		t.Error("no AI-specific groups here — HasExplicitAIRules must be false")
	}

	clean := parseRobotsForAI("# Crawl rules\nUser-agent: *\nAllow: /\n")
	if clean.CommentLineCount != 1 || len(clean.SuspectComments) != 0 {
		t.Errorf("benign comment flagged: count=%d suspect=%v", clean.CommentLineCount, clean.SuspectComments)
	}
}

func TestApplyTextStats_FillerAndDensity(t *testing.T) {
	page := extractDeepPageFeatures(parseFixture(t, `<html><body><main><p>`+
		"In today's fast-paced world, we delve into the topic. It's important to note that we delve into everything. "+
		strings.Repeat("substance detail evidence analysis insight ", 30)+
		`</p></main></body></html>`), "https://example.com/x", false)
	if page.FillerPhraseCount < 2 {
		t.Errorf("filler phrases = %d, want ≥2", page.FillerPhraseCount)
	}
	if page.AIPatternCount < 2 {
		t.Errorf("ai patterns = %d, want ≥2 (two 'delve into')", page.AIPatternCount)
	}
	if page.WordCount == 0 || page.TopKeywordDensityPct <= 0 {
		t.Errorf("wordCount=%d density=%v", page.WordCount, page.TopKeywordDensityPct)
	}
}

func TestSelectDeepPassSample_ClusterRepresentatives(t *testing.T) {
	run := &models.AuditRun{TargetURL: "https://example.com", Kind: string(core.RunKindTenant)}
	var pages []artifacts.CrawlPage
	pages = append(pages,
		artifacts.CrawlPage{URL: "https://example.com/", StatusCode: 200, ClickDepth: 0},
		// Nav chrome: massive inbound counts — the old selection took ONLY these.
		artifacts.CrawlPage{URL: "https://example.com/blogs", StatusCode: 200, ClickDepth: 1, InboundLinksCount: 100},
		artifacts.CrawlPage{URL: "https://example.com/faqs", StatusCode: 200, ClickDepth: 1, InboundLinksCount: 99},
		artifacts.CrawlPage{URL: "https://example.com/about", StatusCode: 200, ClickDepth: 1, InboundLinksCount: 98},
		artifacts.CrawlPage{URL: "https://example.com/contact", StatusCode: 200, ClickDepth: 1, InboundLinksCount: 97},
	)
	// The CMS collection: 20 posts, 1-2 inbound each — never sampled before.
	for i := 0; i < 20; i++ {
		pages = append(pages, artifacts.CrawlPage{
			URL:        fmt.Sprintf("https://example.com/blogs/post-%02d", i),
			StatusCode: 200, ClickDepth: 2, InboundLinksCount: 1, WordCount: 1000 + i,
		})
	}
	// A second, smaller collection.
	for i := 0; i < 5; i++ {
		pages = append(pages, artifacts.CrawlPage{
			URL:        fmt.Sprintf("https://example.com/platform/feature-%d", i),
			StatusCode: 200, ClickDepth: 1, InboundLinksCount: 50, WordCount: 450,
		})
	}

	sample := selectDeepPassSample(run, pages, 8, 4)
	if len(sample) != 8 {
		t.Fatalf("sample size = %d, want 8", len(sample))
	}
	if sample[0] != "https://example.com/" {
		t.Errorf("homepage must lead the sample, got %s", sample[0])
	}
	hasBlogPost, hasPlatform := false, false
	for _, u := range sample {
		if strings.HasPrefix(u, "https://example.com/blogs/") {
			hasBlogPost = true
		}
		if strings.HasPrefix(u, "https://example.com/platform/") {
			hasPlatform = true
		}
	}
	if !hasBlogPost {
		t.Errorf("sample contains no blog post — template cluster unsampled: %v", sample)
	}
	if !hasPlatform {
		t.Errorf("sample contains no platform page — template cluster unsampled: %v", sample)
	}
	// The blog representative must be the most substantive post, not a stub.
	for _, u := range sample {
		if strings.HasPrefix(u, "https://example.com/blogs/") && u != "https://example.com/blogs/post-19" {
			t.Errorf("blog representative = %s, want the highest-wordcount post-19", u)
		}
	}
}

func TestTemplateClusterKey(t *testing.T) {
	cases := map[string]string{
		"https://example.com/blogs/post-a":  "blogs/*",
		"https://example.com/blogs":         "",
		"https://example.com/":              "",
		"https://example.com/blogs?page=2":  "",
		"https://example.com/a/b/c":         "a/*",
		"https://example.com/Platform/Risk": "platform/*",
	}
	for u, want := range cases {
		if got := templateClusterKey(u); got != want {
			t.Errorf("templateClusterKey(%s) = %q, want %q", u, got, want)
		}
	}
}

// Round-4: body-scoped structure extraction — footer/nav headings must never
// masquerade as content structure (the /faqs case: div questions + 6 footer
// h4s passed the heading check).
func TestExtractDeepPageFeatures_BodyScopedStructure(t *testing.T) {
	page := extractFixture(t, `<!DOCTYPE html><html><head><title>FAQs</title></head><body>
<header><h1>Site Masthead</h1></header>
<nav><h4>Products</h4><ul><li><a href="/a">A</a></li><li><a href="/b">B</a></li></ul></nav>
<div class="faq">
  <div class="q">What is an audit?</div>
  <div class="a">Answers live in divs, not headings.</div>
</div>
<footer><h4>Company</h4><h4>Legal</h4><h4>Resources</h4><h4>Social</h4>
<p>`+strings.Repeat("legal word ", 70)+`</p>
<img src="/logo.png"></footer>
</body></html>`)
	if len(page.Headings) != 0 {
		t.Errorf("chrome headings leaked into body structure: %+v", page.Headings)
	}
	if page.QuestionHeadings != 0 {
		t.Errorf("QuestionHeadings = %d, want 0", page.QuestionHeadings)
	}
	if page.AnswerBlockCount != 0 {
		t.Errorf("footer paragraph counted as answer block")
	}
	if page.ListCount != 0 {
		t.Errorf("nav list counted as content list: %d", page.ListCount)
	}
	if len(page.Images) != 0 {
		t.Errorf("footer logo counted as content image: %+v", page.Images)
	}
}

// Inside <article>, a nested <header> legitimately holds the article H1.
func TestExtractDeepPageFeatures_ArticleHeaderH1Kept(t *testing.T) {
	page := extractFixture(t, `<!DOCTYPE html><html><body>
<header><h1>Masthead</h1></header>
<article><header><h1>The Real Article Title</h1></header>
<h2>How does this work in practice?</h2><p>Body text.</p></article>
</body></html>`)
	if page.H1Text != "The Real Article Title" {
		t.Errorf("H1Text = %q, want the article header H1", page.H1Text)
	}
	if page.QuestionHeadings != 1 {
		t.Errorf("QuestionHeadings = %d, want 1", page.QuestionHeadings)
	}
}

func TestLooksLikeHTMLBody(t *testing.T) {
	if !looksLikeHTMLBody([]byte("<!DOCTYPE html><html><body>SPA</body></html>")) {
		t.Error("HTML doc not detected")
	}
	if looksLikeHTMLBody([]byte("User-agent: *\nDisallow:")) {
		t.Error("robots body misdetected as HTML")
	}
	if looksLikeHTMLBody([]byte("# Acme\n> AI platform\n- fact one")) {
		t.Error("llms.txt body misdetected as HTML")
	}
}

func TestParseRobotsForAI_AllowNeutralizesAndWildcard(t *testing.T) {
	// Allow: / wins the tie against Disallow: / — not a full block.
	probe := parseRobotsForAI("User-agent: *\nDisallow: /\nAllow: /\n")
	if probe.DisallowAll {
		t.Error("Allow: / must neutralize Disallow: /")
	}
	// Disallow: /* is a full block.
	probe2 := parseRobotsForAI("User-agent: GPTBot\nDisallow: /*\n")
	if probe2.Crawlers["GPTBot"] {
		t.Error("Disallow: /* must read as blocked for GPTBot")
	}
	// Sitemap directives counted.
	probe3 := parseRobotsForAI("Sitemap: https://x.com/a.xml\nSitemap: https://x.com/a.xml\nUser-agent: *\nDisallow:\n")
	if probe3.SitemapDirectiveCount != 2 {
		t.Errorf("SitemapDirectiveCount = %d, want 2", probe3.SitemapDirectiveCount)
	}
}

func TestSchemaAuthorAndOrgSubtypes(t *testing.T) {
	page := extractFixture(t, `<!DOCTYPE html><html><head>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"LocalBusiness","name":"Acme","sameAs":"https://x.com/acme","author":{"@type":"Organization","name":"Team Acme"}}</script>
<script type="application/ld+json">[]</script>
</head><body><p>hi</p></body></html>`)
	if !page.HasOrganizationSchema {
		t.Error("LocalBusiness must count as Organization-like")
	}
	if page.OrganizationSameAsCount != 1 {
		t.Errorf("string sameAs count = %d, want 1", page.OrganizationSameAsCount)
	}
	if page.HasByline {
		t.Error("Organization-typed author must not count as a byline")
	}
	// The empty-array block is parseable but carries no objects → not valid.
	invalid := 0
	for _, b := range page.SchemaBlocks {
		if !b.Valid {
			invalid++
		}
	}
	if invalid != 1 {
		t.Errorf("invalid blocks = %d, want 1 (the [] block)", invalid)
	}
}

func extractFixture(t *testing.T, raw string) artifacts.DeepPage {
	t.Helper()
	return extractDeepPageFeatures(parseFixture(t, raw), "https://example.com/page", false)
}
