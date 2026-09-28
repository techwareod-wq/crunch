package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

// The canonical threshold numbers from scope §5 become executable assertions
// here — fidelity is testable, not aspirational.

func testInput(t *testing.T, kinds map[core.Kind]any) Input {
	t.Helper()
	b := artifacts.NewBundle()
	for k, v := range kinds {
		b.Put(k, v)
	}
	return Input{
		Bundle: b,
		Run:    &models.AuditRun{TargetDomain: "example.com", TargetURL: "https://example.com"},
		Values: config.AuditValues{},
	}
}

func run(t *testing.T, c Check, in Input) core.CheckResult {
	t.Helper()
	result, err := c.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("%s: %v", c.ID(), err)
	}
	return result
}

func hasFindingContaining(findings []core.Finding, substr string) bool {
	for _, f := range findings {
		if strings.Contains(f.Title, substr) || strings.Contains(f.Detail, substr) {
			return true
		}
	}
	return false
}

// --- onpage.titles: the 30–60 band ---

func TestOnpageTitles_Band(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{
		{URL: "https://example.com/ok", StatusCode: 200, Title: strings.Repeat("a", 45)},
		{URL: "https://example.com/short", StatusCode: 200, Title: strings.Repeat("a", 29)}, // < 30
		{URL: "https://example.com/long", StatusCode: 200, Title: strings.Repeat("a", 61)},  // > 60
		{URL: "https://example.com/none", StatusCode: 200, Title: ""},
		{URL: "https://example.com/redirect", StatusCode: 301, IsRedirect: true}, // never assessed
	}}
	result := run(t, onpageTitles{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))

	// 1 of 4 assessed pages passes.
	if got := result.Score.Ratio(); got != 0.25 {
		t.Errorf("titles ratio = %v, want 0.25", got)
	}
	if !hasFindingContaining(result.Findings, "no title") {
		t.Error("missing-title finding absent")
	}
	if !hasFindingContaining(result.Findings, "too-short") || !hasFindingContaining(result.Findings, "too-long") {
		t.Error("band findings absent")
	}
	// Boundary values are IN band.
	crawl2 := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{
		{URL: "https://example.com/min", StatusCode: 200, Title: strings.Repeat("a", 30)},
		{URL: "https://example.com/max", StatusCode: 200, Title: strings.Repeat("b", 60)},
	}}
	result2 := run(t, onpageTitles{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl2}))
	if got := result2.Score.Ratio(); got != 1 {
		t.Errorf("boundary titles ratio = %v, want 1 (30 and 60 are in-band)", got)
	}
}

// --- onpage.metas: the 120–160 band ---

func TestOnpageMetas_Band(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{
		{URL: "https://example.com/a", StatusCode: 200, MetaDescription: strings.Repeat("x", 120)},
		{URL: "https://example.com/b", StatusCode: 200, MetaDescription: strings.Repeat("y", 160)},
		{URL: "https://example.com/c", StatusCode: 200, MetaDescription: strings.Repeat("z", 119)},
		{URL: "https://example.com/d", StatusCode: 200, MetaDescription: ""},
	}}
	result := run(t, onpageMetas{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))
	if got := result.Score.Ratio(); got != 0.5 {
		t.Errorf("metas ratio = %v, want 0.5 (120 and 160 in-band; 119 and empty fail)", got)
	}
}

// --- performance.cwv: LCP<2.5s / INP<200ms / CLS<0.1, and the no-field-data
// honesty path ---

func TestPerformanceCWV_Thresholds(t *testing.T) {
	psi := &artifacts.PSIArtifact{
		HasFieldData: true,
		OriginLCPMs:  2500, // exactly good
		OriginINPMs:  200,  // exactly good
		OriginCLS:    0.1,  // exactly good
	}
	result := run(t, performanceCWV{}, testInput(t, map[core.Kind]any{artifacts.KindPSI: psi}))
	if got := result.Score.Ratio(); got != 1 {
		t.Errorf("all-good CWV ratio = %v, want 1", got)
	}

	psiPoor := &artifacts.PSIArtifact{HasFieldData: true, OriginLCPMs: 4500, OriginINPMs: 600, OriginCLS: 0.3}
	resultPoor := run(t, performanceCWV{}, testInput(t, map[core.Kind]any{artifacts.KindPSI: psiPoor}))
	if got := resultPoor.Score.Ratio(); got != 0 {
		t.Errorf("all-poor CWV ratio = %v, want 0", got)
	}
	// The findings must speak INP, never FID (negative corpus).
	for _, f := range resultPoor.Findings {
		if strings.Contains(f.Title, "FID") || strings.Contains(f.Detail, "FID") {
			t.Errorf("finding mentions retired FID metric: %q", f.Title)
		}
	}
}

func TestPerformanceCWV_NoFieldData(t *testing.T) {
	psi := &artifacts.PSIArtifact{HasFieldData: false}
	result := run(t, performanceCWV{}, testInput(t, map[core.Kind]any{artifacts.KindPSI: psi}))
	if result.Score != nil {
		t.Error("no field data must yield a nil score (unmeasurable ≠ failing)")
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != core.SeverityInfo {
		t.Error("no-field-data must emit exactly one Info constraint")
	}
}

// --- schema.deprecations: FAQPage is ALWAYS Info, never "remove" ---

func TestSchemaDeprecations_FAQPageIsInfo(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/faq", Fetched: true,
		SchemaBlocks: []artifacts.SchemaBlock{{Types: []string{"FAQPage"}, Valid: true}},
	}}}
	result := run(t, schemaDeprecations{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	var faq *core.Finding
	for i := range result.Findings {
		if strings.Contains(result.Findings[i].Title, "FAQPage") {
			faq = &result.Findings[i]
		}
	}
	if faq == nil {
		t.Fatal("FAQPage finding absent")
	}
	if faq.Severity != core.SeverityInfo {
		t.Errorf("FAQPage severity = %s, MUST be info", faq.Severity)
	}
	lower := strings.ToLower(faq.Recommendation + " " + faq.Detail)
	if strings.Contains(lower, "remove the") || strings.HasPrefix(strings.ToLower(faq.Recommendation), "remove") {
		t.Errorf("FAQPage finding recommends removal: %q", faq.Recommendation)
	}
	// FAQPage doesn't cost score either.
	if got := result.Score.Ratio(); got != 1 {
		t.Errorf("FAQPage-only page scored %v, want 1 (Info never deducts)", got)
	}
}

// --- aisearch.crawler_access: llms.txt is presence-only, zero weight ---

func TestAisearchCrawlerAccess_LlmsTxtZeroWeight(t *testing.T) {
	base := func(llms bool) Input {
		return testInput(t, map[core.Kind]any{
			artifacts.KindCrawl: &artifacts.CrawlArtifact{},
			artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
				Robots:       artifacts.RobotsProbe{Found: true, Crawlers: map[string]bool{"GPTBot": true, "ClaudeBot": true}},
				LlmsTxtFound: llms,
			},
		})
	}
	with := run(t, aisearchCrawlerAccess{}, base(true))
	without := run(t, aisearchCrawlerAccess{}, base(false))
	if with.Score.Ratio() != without.Score.Ratio() {
		t.Errorf("llms.txt presence changed the score (%v vs %v) — it must carry ZERO ranking weight",
			with.Score.Ratio(), without.Score.Ratio())
	}
	if !hasFindingContaining(without.Findings, "llms.txt") {
		t.Error("llms.txt absence should still be REPORTED (presence-only)")
	}
}

func TestAisearchCrawlerAccess_BlockedCrawlers(t *testing.T) {
	in := testInput(t, map[core.Kind]any{
		artifacts.KindCrawl: &artifacts.CrawlArtifact{},
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
			Robots: artifacts.RobotsProbe{Found: true, Crawlers: map[string]bool{
				"GPTBot": false, "ClaudeBot": true, "PerplexityBot": true, "CCBot": true,
			}},
			LlmsTxtFound: true,
		},
	})
	result := run(t, aisearchCrawlerAccess{}, in)
	if got := result.Score.Ratio(); got != 0.75 {
		t.Errorf("crawler access ratio = %v, want 0.75 (3 of 4 allowed)", got)
	}
}

// --- aisearch.answer_blocks: 134–167 words is the citability band ---

func TestAisearchAnswerBlocks(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/", Fetched: true, IsHomepage: true}, // homepage exempt
		{URL: "https://example.com/a", Fetched: true, AnswerBlockCount: 2},
		{URL: "https://example.com/b", Fetched: true, AnswerBlockCount: 0},
	}}
	result := run(t, aisearchAnswerBlocks{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if got := result.Score.Ratio(); got != 0.5 {
		t.Errorf("answer blocks ratio = %v, want 0.5", got)
	}
	if !hasFindingContaining(result.Findings, "134") {
		t.Error("finding must cite the canonical 134–167-word band")
	}
}

// --- content.parasite_markers: intrinsically findings-only ---

func TestContentParasiteMarkers_FindingsOnly(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/casino-bonus", Fetched: true,
		ParasiteMarkers: []string{"path:casino"},
	}}}
	result := run(t, contentParasiteMarkers{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if result.Score != nil {
		t.Error("parasite markers must NEVER contribute a score (Score nil)")
	}
	if len(result.Findings) == 0 || result.Findings[0].Severity != core.SeverityHigh {
		t.Error("parasite markers must surface a High finding")
	}
	if result.Evidence == nil {
		t.Error("parasite evidence must flow to the LLM rubric (decision 12)")
	}
}

// --- quality-uplift regressions (the four tryelip false negatives + the new
// checks' core behaviors) ---

// TestContentInternalLinking_IslandFinding: a post set whose bodies carry
// zero cross-content links must trigger the High island finding (uplift 1.1).
func TestContentInternalLinking_IslandFinding(t *testing.T) {
	deepPage := func(url string) artifacts.DeepPage {
		return artifacts.DeepPage{
			URL: url, Fetched: true, WordCount: 800,
			BodyLinksAssessed:     true,
			BodyInternalLinkCount: 1,
			// Only the blog index — not another content page.
			BodyInternalTargets: []string{"https://example.com/blog"},
		}
	}
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		deepPage("https://example.com/blog/a"),
		deepPage("https://example.com/blog/b"),
		deepPage("https://example.com/blog/c"),
	}}
	in := testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    &artifacts.CrawlArtifact{},
		artifacts.KindHTMLDeep: deep,
	})
	result := run(t, contentInternalLinking{}, in)
	if !hasFindingContaining(result.Findings, "island") {
		t.Errorf("island finding absent; findings: %+v", result.Findings)
	}
	if got := result.Score.Ratio(); got != 0 {
		t.Errorf("island score ratio = %v, want 0 (no page passes)", got)
	}

	// Cross-linked posts pass and produce no island.
	linked := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/blog/a", Fetched: true, WordCount: 800, BodyLinksAssessed: true,
			BodyInternalLinkCount: 3, BodyInternalTargets: []string{"https://example.com/blog/b", "https://example.com/blog"}},
		{URL: "https://example.com/blog/b", Fetched: true, WordCount: 800, BodyLinksAssessed: true,
			BodyInternalLinkCount: 2, BodyInternalTargets: []string{"https://example.com/blog/a"}},
		{URL: "https://example.com/blog/c", Fetched: true, WordCount: 800, BodyLinksAssessed: true,
			BodyInternalLinkCount: 2, BodyInternalTargets: []string{"https://example.com/blog/a"}},
	}}
	healthy := run(t, contentInternalLinking{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    &artifacts.CrawlArtifact{},
		artifacts.KindHTMLDeep: linked,
	}))
	if hasFindingContaining(healthy.Findings, "island") {
		t.Error("cross-linked posts must not trigger the island finding")
	}
	if got := healthy.Score.Ratio(); got != 1 {
		t.Errorf("cross-linked score ratio = %v, want 1", got)
	}
}

// TestContentTrustSignals_BodyCitations: nav-count citations were the 1.2
// false negative — body-scoped zero citations must fail even when the raw
// external count is high, and 1500+ uncited words earns the Medium finding.
func TestContentTrustSignals_BodyCitations(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/blog/long", Fetched: true, WordCount: 2762,
		HasByline:         true,
		ExternalLinkCount: 5, // socials + CTAs, page-wide
		BodyLinksAssessed: true,
		BodyCitationCount: 0,
	}}}
	result := run(t, contentTrustSignals{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if !hasFindingContaining(result.Findings, "cite no sources") {
		t.Errorf("long-form zero-citation Medium finding absent; findings: %+v", result.Findings)
	}
	if result.Evidence["citationShare"].(float64) != 0 {
		t.Errorf("citationShare = %v, want 0 (body-scoped)", result.Evidence["citationShare"])
	}
}

// TestTechnicalSitemap_PartialLastmod is the 1.3 regression: 18/24 lastmod
// must score partial, list the uncovered URLs, and flag priority noise.
func TestTechnicalSitemap_PartialLastmod(t *testing.T) {
	sm := artifacts.SitemapProbe{
		Found: true, URL: "https://example.com/sitemap.xml",
		EntryCount: 24, HasLastmod: true, LastmodCount: 18, PriorityCount: 24,
	}
	for i := 0; i < 24; i++ {
		e := artifacts.SitemapProbeEntry{URL: fmt.Sprintf("https://example.com/p%d", i)}
		if i < 18 {
			e.LastMod = "2026-08-01"
		}
		sm.Entries = append(sm.Entries, e)
	}
	deep := &artifacts.HTMLDeepArtifact{Sitemap: sm}
	result := run(t, technicalSitemap{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))

	// found 40 + entries 20 + 40×(18/24) = 90.
	if got := result.Score.Earned; got != 90 {
		t.Errorf("partial-lastmod score = %v, want 90", got)
	}
	if !hasFindingContaining(result.Findings, "6 of 24") {
		t.Errorf("partial-lastmod finding absent; findings: %+v", result.Findings)
	}
	if !hasFindingContaining(result.Findings, "priority") && !hasFindingContaining(result.Findings, "<priority>") {
		t.Error("priority/changefreq noise Info finding absent")
	}
}

// TestTechnicalCanonicalization_StringMismatch is the 1.4 regression:
// canonical https://x.com on page https://x.com/ (normalized-equal,
// string-unequal) must flag at half weight.
func TestTechnicalCanonicalization_StringMismatch(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{{
		URL: "https://example.com/", StatusCode: 200, Canonical: "https://example.com",
	}}}
	result := run(t, technicalCanonicalization{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))
	if !hasFindingContaining(result.Findings, "string-match") {
		t.Errorf("string-mismatch finding absent; findings: %+v", result.Findings)
	}
	if got := result.Score.Earned; got != 50 {
		t.Errorf("mismatch score = %v, want 50 (half weight on 1 assessed page)", got)
	}

	// A canonical to a genuinely DIFFERENT page stays untouched.
	variant := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{{
		URL: "https://example.com/page?ref=x", StatusCode: 200, Canonical: "https://example.com/page",
	}}}
	clean := run(t, technicalCanonicalization{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: variant}))
	if hasFindingContaining(clean.Findings, "string-match") {
		t.Error("legit variant consolidation flagged as mismatch")
	}
}

// TestOnpageHeadings_H1Suspect: a corrupted H1 counts against the score with
// a Medium finding (uplift 2.1) — read opportunistically off the deep pass.
func TestOnpageHeadings_H1Suspect(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{Pages: []artifacts.CrawlPage{
		{URL: "https://example.com/", StatusCode: 200, H1Count: 1},
		{URL: "https://example.com/ok", StatusCode: 200, H1Count: 1},
	}}
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/", Fetched: true,
		H1Text: "Meet Elip yourjob agent in WhatsAppiMessagesoonWhatsApp", H1Suspect: true, H1SuspectReason: "concatenated_fragments",
	}}}
	result := run(t, onpageHeadings{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl: crawl, artifacts.KindHTMLDeep: deep,
	}))
	if !hasFindingContaining(result.Findings, "corrupted") {
		t.Errorf("H1-corruption finding absent; findings: %+v", result.Findings)
	}
	// Round 4: the deep-sample suspect signal is findings-only — mixing the
	// tiny deep sample into the full-crawl denominator misstated prevalence.
	if got := result.Score.Ratio(); got != 1 {
		t.Errorf("suspect-H1 ratio = %v, want 1 (suspect pages are findings-only)", got)
	}
	// Without the deep pass the check still runs (opportunistic read).
	noDeep := run(t, onpageHeadings{}, testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl}))
	if got := noDeep.Score.Ratio(); got != 1 {
		t.Errorf("no-deep ratio = %v, want 1", got)
	}
}

// TestAisearchRenderedParity: <20% raw/rendered is High-invisible; failed
// raw fetches are skipped, never guessed (uplift 2.2).
func TestAisearchRenderedParity(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/a", Fetched: true, WordCount: 1000, RawFetchOK: true, RawWordCount: 50},  // invisible
		{URL: "https://example.com/b", Fetched: true, WordCount: 1000, RawFetchOK: true, RawWordCount: 400}, // degraded
		{URL: "https://example.com/c", Fetched: true, WordCount: 1000, RawFetchOK: true, RawWordCount: 900}, // pass
		{URL: "https://example.com/d", Fetched: true, WordCount: 1000, RawFetchOK: false, RawWordCount: 0},  // skipped
	}}
	result := run(t, aisearchRenderedParity{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if got := result.Score.Ratio(); got*3 != 1 {
		t.Errorf("parity ratio = %v, want 1/3 (1 of 3 assessed passes; the failed fetch is skipped)", got)
	}
	if !hasFindingContaining(result.Findings, "invisible without JavaScript") {
		t.Errorf("invisible-without-JS High finding absent; findings: %+v", result.Findings)
	}
}

// TestTechnicalSoft404: a 200 for a guaranteed-nonexistent path is the High
// failure; a real 404 is full marks (uplift 3.3).
func TestTechnicalSoft404(t *testing.T) {
	soft := run(t, technicalSoft404{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Soft404: artifacts.Soft404Probe{Fetched: true, Status: 200, FinalStatus: 200},
	}}))
	if soft.Score.Earned != 0 || !hasFindingContaining(soft.Findings, "soft-404") {
		t.Errorf("soft-404 must score 0 with the High finding; got %v / %+v", soft.Score.Earned, soft.Findings)
	}
	real404 := run(t, technicalSoft404{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Soft404: artifacts.Soft404Probe{Fetched: true, Status: 404, FinalStatus: 404},
	}}))
	if real404.Score.Earned != 100 || len(real404.Findings) != 0 {
		t.Errorf("real 404 must score 100 clean; got %v / %+v", real404.Score.Earned, real404.Findings)
	}
	redirect := run(t, technicalSoft404{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Soft404: artifacts.Soft404Probe{Fetched: true, Status: 301, FinalStatus: 200},
	}}))
	if redirect.Score.Earned != 40 {
		t.Errorf("redirect-to-200 must score 40; got %v", redirect.Score.Earned)
	}
}

// TestAisearchBrandFootprint: zero third-party mentions is the High
// structural failure; the sameAs cross-reference fires when mentions exist
// but aren't declared (uplift 3.1).
func TestAisearchBrandFootprint(t *testing.T) {
	zero := run(t, aisearchBrandFootprint{}, testInput(t, map[core.Kind]any{
		artifacts.KindMentions: &artifacts.MentionsArtifact{QueriedTerms: []string{`"example.com"`}},
	}))
	if zero.Score.Earned != 0 || !hasFindingContaining(zero.Findings, "nothing to cite") {
		t.Errorf("zero footprint must score 0 with the High finding; got %v / %+v", zero.Score.Earned, zero.Findings)
	}

	some := run(t, aisearchBrandFootprint{}, testInput(t, map[core.Kind]any{
		artifacts.KindMentions: &artifacts.MentionsArtifact{
			ThirdPartyCount: 4, ThirdPartyDomains: []string{"a.com", "b.com", "c.com", "d.com"},
		},
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
			URL: "https://example.com/", Fetched: true, HasOrganizationSchema: true, OrganizationSameAsCount: 0,
		}}},
	}))
	if some.Score.Earned != 70 {
		t.Errorf("3–5 domains must score 70; got %v", some.Score.Earned)
	}
	if !hasFindingContaining(some.Findings, "sameAs") {
		t.Errorf("undeclared-sameAs cross-reference absent; findings: %+v", some.Findings)
	}
}

// TestSchemaCoverage: a naked homepage next to exemplary articles is the
// inverted priority (uplift 2.3).
func TestSchemaCoverage_InvertedPriority(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/", Fetched: true, IsHomepage: true}, // no schema at all
		{URL: "https://example.com/blog/a", Fetched: true, HasByline: true, HasDates: true,
			SchemaBlocks: []artifacts.SchemaBlock{{Types: []string{"BlogPosting"}, Valid: true}}},
	}}
	result := run(t, schemaCoverage{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	// Homepage 0/60 + articles 40/40 = 40/100.
	if got := result.Score.Ratio(); got != 0.4 {
		t.Errorf("coverage ratio = %v, want 0.4", got)
	}
	if !hasFindingContaining(result.Findings, "entity-level schema") {
		t.Errorf("homepage entity-schema finding absent; findings: %+v", result.Findings)
	}
}

// TestContentScaledPatterns: burst + template uniformity trips the Medium
// pattern finding with corrective (non-deletion) framing (uplift 2.5).
func TestContentScaledPatterns(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{}
	sm := artifacts.SitemapProbe{Found: true, EntryCount: 12, HasLastmod: true}
	for i := 0; i < 12; i++ {
		crawl.Pages = append(crawl.Pages, artifacts.CrawlPage{
			URL: fmt.Sprintf("https://example.com/blog/p%d", i), StatusCode: 200, WordCount: 900,
			Title: fmt.Sprintf("%d Best Tools for Marketers (2026 Guide)", i+3),
		})
		sm.Entries = append(sm.Entries, artifacts.SitemapProbeEntry{
			URL: fmt.Sprintf("https://example.com/blog/p%d", i), LastMod: fmt.Sprintf("2026-08-%02d", (i%5)+1),
		})
		sm.LastmodCount++
	}
	result := run(t, contentScaledPatterns{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    crawl,
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Sitemap: sm},
	}))
	if result.Score.Earned >= 100 {
		t.Errorf("scaled patterns score = %v, want < 100 (burst + skeleton + hook + listicle)", result.Score.Earned)
	}
	// The fixture's titles differ only in their leading number, so both the
	// fingerprint finding AND the template-twin finding fire.
	if len(result.Findings) != 2 {
		t.Fatalf("want fingerprint + template-twin findings, got %+v", result.Findings)
	}
	if !hasFindingContaining(result.Findings, "fingerprint") || !hasFindingContaining(result.Findings, "template twins") {
		t.Fatalf("missing fingerprint/template-twin finding, got %+v", result.Findings)
	}
	for _, f := range result.Findings {
		if f.Severity != core.SeverityMedium {
			t.Errorf("finding %q severity = %s, want medium", f.Title, f.Severity)
		}
		if strings.Contains(f.Title, "fingerprint") && !strings.Contains(f.Recommendation, "not deletion") {
			t.Errorf("recommendation must carry the corrective framing, got %q", f.Recommendation)
		}
	}
}

// TestTitlesAreTwins: the pair heuristic — ≤2 swapped tokens on ≥6-token
// titles; identical titles and short titles are out of scope.
func TestTitlesAreTwins(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"7 Best Job Agencies in Ernakulam for Tech Professionals", "7 Best Job Agencies in Amritsar for Tech Professionals", true},
		{"7 Best Job Agencies in Ernakulam for Tech Professionals", "7 Best Job Agencies in Ernakulam for Tech Professionals", false}, // identical → duplicate-title turf
		{"Short title here", "Short title there", false}, // < 6 tokens
		{"How to Write a Relieving Letter Format That HR Accepts", "15 Resume Software Skills Developers Should List in 2026", false},
	}
	for _, c := range cases {
		if got := titlesAreTwins(c.a, c.b); got != c.want {
			t.Errorf("titlesAreTwins(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestOnpageHeadHygiene: og+twitter:card+twitter:site+lang weights
// (40/15/5/40) per page; a card without a site handle costs its 5 and fires
// the attribution finding.
func TestOnpageHeadHygiene(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/a", Fetched: true, HasOGTitle: true, HasOGImage: true, HasTwitterCard: true, PageLang: "en"},
		{URL: "https://example.com/b", Fetched: true}, // nothing
	}}
	result := run(t, onpageHeadHygiene{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if got := result.Score.Earned; got != 47.5 {
		t.Errorf("head hygiene score = %v, want 47.5 (95 + 0 over 2 pages)", got)
	}
	if !hasFindingContaining(result.Findings, "Open Graph") || !hasFindingContaining(result.Findings, "lang") {
		t.Errorf("missing OG/lang findings; findings: %+v", result.Findings)
	}
	if !hasFindingContaining(result.Findings, "twitter:site") {
		t.Errorf("missing twitter:site finding; findings: %+v", result.Findings)
	}

	full := run(t, onpageHeadHygiene{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Pages: []artifacts.DeepPage{{URL: "https://example.com/a", Fetched: true,
			HasOGTitle: true, HasOGImage: true, HasTwitterCard: true, HasTwitterSite: true, PageLang: "en"}},
	}}))
	if full.Score.Earned != 100 {
		t.Errorf("full head hygiene = %v, want 100", full.Score.Earned)
	}
	if hasFindingContaining(full.Findings, "twitter:site") {
		t.Errorf("twitter:site finding must not fire when the handle is present")
	}
}

// TestTechnicalSecurityHeaders: the 30/25/15/15/15 split and the skip path.
func TestTechnicalSecurityHeaders(t *testing.T) {
	all := run(t, technicalSecurityHeaders{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Headers: artifacts.HeadersProbe{Fetched: true, HSTS: true, CSP: true, XContentTypeOptions: true, FrameProtection: true, ReferrerPolicy: true},
	}}))
	if all.Score.Earned != 100 {
		t.Errorf("all headers = %v, want 100", all.Score.Earned)
	}
	hstsOnly := run(t, technicalSecurityHeaders{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
		Headers: artifacts.HeadersProbe{Fetched: true, HSTS: true},
	}}))
	if hstsOnly.Score.Earned != 30 {
		t.Errorf("HSTS-only = %v, want 30", hstsOnly.Score.Earned)
	}
	unfetched := run(t, technicalSecurityHeaders{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{}}))
	if len(unfetched.Findings) != 0 {
		t.Error("unfetched probe must not emit findings (never guess)")
	}
}

// --- every finding carries falsifiability (mandatory IP) ---

func TestEveryFinding_HasFalsifiability(t *testing.T) {
	crawl := &artifacts.CrawlArtifact{
		InternalLinksTotal: 10,
		BrokenLinks:        []artifacts.BrokenLink{{FromURL: "https://example.com/", ToURL: "https://example.com/dead"}},
		Pages: []artifacts.CrawlPage{
			{URL: "https://example.com/x", StatusCode: 404},
			{URL: "https://example.com/y", StatusCode: 200, Title: "t", NoIndex: true},
		},
	}
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/y", Fetched: true, WordCount: 50,
		Images: []artifacts.DeepImage{{Src: "https://example.com/i.png", HasAlt: false}},
	}}}
	in := testInput(t, map[core.Kind]any{artifacts.KindCrawl: crawl, artifacts.KindHTMLDeep: deep})

	for _, c := range []Check{
		technicalBrokenLinks{}, technicalRedirectsStatus{}, technicalIndexabilityRobots{},
		technicalSitemap{}, technicalIndexNow{}, contentSubstance{}, imagesAltSemantics{},
	} {
		result := run(t, c, in)
		for _, f := range result.Findings {
			if strings.TrimSpace(f.Falsifiability) == "" {
				t.Errorf("check %s finding %q has no falsifiability — every recommendation must carry one", c.ID(), f.Title)
			}
		}
	}
}

// --- external-audit gap closures (2026-08-30): listing-page island defeat,
// homepage link/imagery signals, llms.txt key facts, explicit AI directives ---

// TestContentInternalLinking_ListingPageCannotDefeatIsland: a /blog listing
// page whose "body" is the post list used to count as a cross-linked content
// page, masking a fully orphaned post set.
func TestContentInternalLinking_ListingPageCannotDefeatIsland(t *testing.T) {
	orphan := func(url string) artifacts.DeepPage {
		return artifacts.DeepPage{
			URL: url, Fetched: true, WordCount: 800, BodyLinksAssessed: true,
			BodyInternalLinkCount: 1, BodyInternalTargets: []string{"https://example.com/blog"},
		}
	}
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		{URL: "https://example.com/blog", Fetched: true, WordCount: 900, BodyLinksAssessed: true,
			BodyInternalLinkCount: 18, BodyInternalTargets: []string{
				"https://example.com/blog/a", "https://example.com/blog/b", "https://example.com/blog/c",
			}},
		orphan("https://example.com/blog/a"),
		orphan("https://example.com/blog/b"),
		orphan("https://example.com/blog/c"),
	}}
	result := run(t, contentInternalLinking{}, testInput(t, map[core.Kind]any{
		artifacts.KindCrawl:    &artifacts.CrawlArtifact{},
		artifacts.KindHTMLDeep: deep,
	}))
	if !hasFindingContaining(result.Findings, "island") {
		t.Errorf("listing page defeated the island detector; findings: %+v", result.Findings)
	}
	if got := result.Score.Ratio(); got != 0 {
		t.Errorf("island score ratio = %v, want 0 (the listing page must not be assessed)", got)
	}
}

// TestContentInternalLinking_HomepageSignal: a homepage whose body links only
// section indexes gets the "links no individual content page" nudge; one
// direct post link clears it.
func TestContentInternalLinking_HomepageSignal(t *testing.T) {
	post := func(url string, targets ...string) artifacts.DeepPage {
		return artifacts.DeepPage{
			URL: url, Fetched: true, WordCount: 800, BodyLinksAssessed: true,
			BodyInternalLinkCount: len(targets), BodyInternalTargets: targets,
		}
	}
	build := func(homeTargets []string) Input {
		deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
			{URL: "https://example.com/", Fetched: true, IsHomepage: true, WordCount: 600,
				BodyLinksAssessed: true, BodyInternalLinkCount: len(homeTargets), BodyInternalTargets: homeTargets},
			post("https://example.com/blog/a", "https://example.com/blog/b"),
			post("https://example.com/blog/b", "https://example.com/blog/a"),
			post("https://example.com/blog/c", "https://example.com/blog/a", "https://example.com/blog/b"),
		}}
		return testInput(t, map[core.Kind]any{
			artifacts.KindCrawl:    &artifacts.CrawlArtifact{},
			artifacts.KindHTMLDeep: deep,
		})
	}

	bare := run(t, contentInternalLinking{}, build([]string{"https://example.com/blog"}))
	if !hasFindingContaining(bare.Findings, "Homepage links to no individual content page") {
		t.Errorf("homepage nudge absent; findings: %+v", bare.Findings)
	}
	linked := run(t, contentInternalLinking{}, build([]string{"https://example.com/blog/a"}))
	if hasFindingContaining(linked.Findings, "Homepage links to no individual content page") {
		t.Error("homepage nudge must not fire when a post is linked directly")
	}
}

// TestAisearchMultimodal_HomepageAndVideo: a homepage with zero <img> and no
// video gets its own Medium finding; a sample with no video anywhere gets the
// Info nudge; neither affects the article score.
func TestAisearchMultimodal_HomepageAndVideo(t *testing.T) {
	richArticle := artifacts.DeepPage{
		URL: "https://example.com/a", Fetched: true,
		Images: []artifacts.DeepImage{{Src: "https://example.com/i.png"}}, ListCount: 2,
	}
	bare := run(t, aisearchMultimodal{}, testInput(t, map[core.Kind]any{
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
			{URL: "https://example.com/", Fetched: true, IsHomepage: true},
			richArticle,
		}},
	}))
	if !hasFindingContaining(bare.Findings, "Homepage carries no real content images") {
		t.Errorf("bare-homepage finding absent; findings: %+v", bare.Findings)
	}
	if !hasFindingContaining(bare.Findings, "No video anywhere") {
		t.Errorf("no-video nudge absent; findings: %+v", bare.Findings)
	}
	if got := bare.Score.Ratio(); got != 1 {
		t.Errorf("homepage findings changed the article score: ratio %v, want 1", got)
	}

	withImage := run(t, aisearchMultimodal{}, testInput(t, map[core.Kind]any{
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
			{URL: "https://example.com/", Fetched: true, IsHomepage: true,
				Images: []artifacts.DeepImage{{Src: "https://example.com/hero.png"}}, VideoEmbedCount: 1},
			richArticle,
		}},
	}))
	if hasFindingContaining(withImage.Findings, "Homepage carries no real images") {
		t.Error("homepage finding must not fire when the homepage has imagery")
	}
	if hasFindingContaining(withImage.Findings, "No video anywhere") {
		t.Error("no-video nudge must not fire when any page embeds video")
	}
}

// TestAisearchCrawlerAccess_KeyFactsAndExplicitRules: the two zero-weight
// nudges — a covered llms.txt without a key-facts block, and AI crawlers
// allowed only implicitly.
func TestAisearchCrawlerAccess_KeyFactsAndExplicitRules(t *testing.T) {
	build := func(keyFacts, explicit bool) Input {
		return testInput(t, map[core.Kind]any{
			artifacts.KindCrawl: &artifacts.CrawlArtifact{},
			artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{
				Robots: artifacts.RobotsProbe{Found: true, HasExplicitAIRules: explicit,
					Crawlers: map[string]bool{"GPTBot": true, "ClaudeBot": true}},
				LlmsTxtFound: true,
				LlmsTxt: artifacts.LlmsTxtProbe{Found: true, LinkCount: 5,
					HasHomepageLink: true, LinksAboutOrProduct: true, HasKeyFacts: keyFacts},
			},
		})
	}

	nudged := run(t, aisearchCrawlerAccess{}, build(false, false))
	if !hasFindingContaining(nudged.Findings, "key facts") {
		t.Errorf("key-facts nudge absent; findings: %+v", nudged.Findings)
	}
	if !hasFindingContaining(nudged.Findings, "allowed only implicitly") {
		t.Errorf("explicit-AI-rules nudge absent; findings: %+v", nudged.Findings)
	}
	for _, f := range nudged.Findings {
		if f.Severity != core.SeverityInfo {
			t.Errorf("nudge %q severity = %s, want info (zero weight)", f.Title, f.Severity)
		}
	}
	if nudged.Score.Ratio() != 1 {
		t.Errorf("nudges changed the score: ratio %v, want 1", nudged.Score.Ratio())
	}

	quiet := run(t, aisearchCrawlerAccess{}, build(true, true))
	if hasFindingContaining(quiet.Findings, "key facts") || hasFindingContaining(quiet.Findings, "allowed only implicitly") {
		t.Errorf("nudges fired despite key facts + explicit rules; findings: %+v", quiet.Findings)
	}
}

// --- aisearch.brand_footprint: category-occupancy finding ---

func TestBrandFootprint_CategoryOccupancy(t *testing.T) {
	base := func(m artifacts.MentionsArtifact) Input {
		return testInput(t, map[core.Kind]any{artifacts.KindMentions: &m})
	}
	occupied := artifacts.MentionsArtifact{
		ThirdPartyCount: 3, ThirdPartyDomains: []string{"a.com", "b.com", "c.com"},
		CategoryProbed: true, CategoryQuery: "job agent in whatsapp",
		CategoryTopDomains: []string{"rival1.com", "rival2.com", "rival3.com", "rival4.com", "rival5.com"},
	}
	result := run(t, aisearchBrandFootprint{}, base(occupied))
	if !hasFindingContaining(result.Findings, "category query") {
		t.Errorf("occupancy finding absent when target missing from a full SERP; findings: %+v", result.Findings)
	}

	ranked := occupied
	ranked.CategoryTargetPosition = 4
	if r := run(t, aisearchBrandFootprint{}, base(ranked)); hasFindingContaining(r.Findings, "category query") {
		t.Error("occupancy finding fired although the target ranks for the query")
	}

	thin := occupied
	thin.CategoryTopDomains = thin.CategoryTopDomains[:3]
	if r := run(t, aisearchBrandFootprint{}, base(thin)); hasFindingContaining(r.Findings, "category query") {
		t.Error("occupancy finding fired on a thin SERP (<5 occupants proves nothing)")
	}

	legacy := occupied
	legacy.CategoryProbed = false
	if r := run(t, aisearchBrandFootprint{}, base(legacy)); hasFindingContaining(r.Findings, "category query") {
		t.Error("occupancy finding fired on an unprobed (legacy) artifact")
	}
}

// --- onpage.head_hygiene: bare lang="en" vs resolved non-US market ---

func TestHeadHygiene_MarketLangNudge(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/", Fetched: true, IsHomepage: true,
		HasOGTitle: true, HasOGImage: true, HasTwitterCard: true, HasTwitterSite: true, PageLang: "en",
	}}}

	in := testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})
	in.Run.LocationCode = 2356 // India
	in.Run.LocationName = "India"
	result := run(t, onpageHeadHygiene{}, in)
	if !hasFindingContaining(result.Findings, "targeting India") {
		t.Errorf("market-lang nudge absent for bare en + India market; findings: %+v", result.Findings)
	}

	us := testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})
	us.Run.LocationCode = usLocationCode
	if r := run(t, onpageHeadHygiene{}, us); hasFindingContaining(r.Findings, "targeting") {
		t.Error("market-lang nudge fired for the US default market")
	}

	unresolved := testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep})
	if r := run(t, onpageHeadHygiene{}, unresolved); hasFindingContaining(r.Findings, "targeting") {
		t.Error("market-lang nudge fired with no resolved market")
	}

	regional := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/", Fetched: true, IsHomepage: true,
		HasOGTitle: true, HasOGImage: true, HasTwitterCard: true, HasTwitterSite: true, PageLang: "en-IN",
	}}}
	rin := testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: regional})
	rin.Run.LocationCode = 2356
	rin.Run.LocationName = "India"
	if r := run(t, onpageHeadHygiene{}, rin); hasFindingContaining(r.Findings, "targeting India") {
		t.Error("market-lang nudge fired although the lang already carries a region")
	}
}

// --- performance.cwv: animation-heavy homepage nudge (no field data) ---

func TestPerformanceCWV_AnimationNudge(t *testing.T) {
	psi := &artifacts.PSIArtifact{HasFieldData: false}
	heavy := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/", Fetched: true, IsHomepage: true, InlineSVGCount: 12, AnimationHintCount: 2,
	}}}
	result := run(t, performanceCWV{}, testInput(t, map[core.Kind]any{
		artifacts.KindPSI: psi, artifacts.KindHTMLDeep: heavy,
	}))
	if !hasFindingContaining(result.Findings, "Animation-heavy") {
		t.Errorf("animation nudge absent for an SVG-heavy homepage; findings: %+v", result.Findings)
	}

	calm := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/", Fetched: true, IsHomepage: true, InlineSVGCount: 2,
	}}}
	if r := run(t, performanceCWV{}, testInput(t, map[core.Kind]any{
		artifacts.KindPSI: psi, artifacts.KindHTMLDeep: calm,
	})); hasFindingContaining(r.Findings, "Animation-heavy") {
		t.Error("animation nudge fired on a calm homepage")
	}
}

// --- onpage.sxo_mismatch: page-type consensus (SXO uplift) ---

func TestSXOMismatch_PageTypeConsensus(t *testing.T) {
	if got := classifySERPPageType("7 Best Job Boards for Developers", "https://a.com/blog/best-job-boards"); got != "listicle" {
		t.Errorf("listicle classification = %q", got)
	}
	if got := classifySERPPageType("How to Write a Resume", "https://a.com/blog/x"); got != "how-to" {
		t.Errorf("how-to classification = %q", got)
	}
	if got := classifySERPPageType("Acme — Hiring Platform", "https://acme.com/"); got != "landing" {
		t.Errorf("landing classification = %q", got)
	}
	if got := classifySERPPageType("Salary Calculator", "https://a.com/tools/salary"); got != "tool" {
		t.Errorf("tool classification = %q", got)
	}

	// 5/5 landing pages rank; our page is a blog article → High (intent boundary).
	results := []artifacts.SXOResult{
		{Rank: 1, Domain: "r1.com", Title: "R1 — Job Platform", URL: "https://r1.com/"},
		{Rank: 2, Domain: "r2.com", Title: "R2 Hiring Software", URL: "https://r2.com/"},
		{Rank: 3, Domain: "r3.com", Title: "R3 Recruiting", URL: "https://r3.com/"},
		{Rank: 4, Domain: "r4.com", Title: "R4 ATS", URL: "https://r4.com/"},
		{Rank: 5, Domain: "r5.com", Title: "R5 Talent Cloud", URL: "https://r5.com/"},
	}
	serp := &artifacts.SERPArtifact{Pages: []artifacts.SXOPage{{
		URL: "https://example.com/blog/hiring-guide", Keyword: "hiring platform", Position: 9,
		ItemTypes: []string{"organic"}, TopResults: results,
		PAAQuestions: []string{"what is a hiring platform?"},
	}}}
	// Round 4: consensus classification needs a real page title (fetched deep
	// page or crawl title) — URL-shape-only classification produced High
	// findings about pages never inspected.
	deepTitles := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
		URL: "https://example.com/blog/hiring-guide", Fetched: true, Title: "The Hiring Guide — Blog",
	}}}
	result := run(t, onpageSXOMismatch{}, testInput(t, map[core.Kind]any{
		artifacts.KindSERP: serp, artifacts.KindHTMLDeep: deepTitles,
	}))
	found := false
	for _, f := range result.Findings {
		if strings.Contains(f.Title, "Page type doesn't match") {
			found = true
			if f.Severity != core.SeverityHigh {
				t.Errorf("intent-boundary mismatch severity = %s, want high", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("consensus mismatch finding absent; findings: %+v", result.Findings)
	}
	if result.Evidence["paaQuestions"] == nil || result.Evidence["serpConsensus"] == nil {
		t.Error("PAA/consensus evidence absent")
	}

	// Thin SERP (4 results) → no consensus finding.
	thin := &artifacts.SERPArtifact{Pages: []artifacts.SXOPage{{
		URL: "https://example.com/blog/x", Keyword: "k", ItemTypes: []string{"organic"}, TopResults: results[:4],
	}}}
	if r := run(t, onpageSXOMismatch{}, testInput(t, map[core.Kind]any{artifacts.KindSERP: thin})); hasFindingContaining(r.Findings, "Page type doesn't match") {
		t.Error("consensus finding fired on a thin result set")
	}
}

// --- aisearch.rendered_parity: fallback documents are never assessed ---

func TestRenderedParity_SkipsFallbackDocs(t *testing.T) {
	deep := &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{
		// The "rendered" doc came from the no-JS fallback: raw-vs-raw would
		// trivially pass — must be skipped, not scored.
		{URL: "https://example.com/a", Fetched: true, WordCount: 500,
			RawFetchOK: true, RawWordCount: 500, RawFallback: true},
	}}
	result := run(t, aisearchRenderedParity{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	// Round 4: an all-skipped sample is unassessed — no score, never a free 100.
	if result.Score != nil || len(result.Findings) != 0 {
		t.Errorf("fallback-only sample must be unscored; got %v findings %d", result.Score, len(result.Findings))
	}

	// A genuinely rendered CSR page alongside it still gets flagged.
	deep.Pages = append(deep.Pages, artifacts.DeepPage{
		URL: "https://example.com/b", Fetched: true, WordCount: 1000,
		RawFetchOK: true, RawWordCount: 100,
	})
	result2 := run(t, aisearchRenderedParity{}, testInput(t, map[core.Kind]any{artifacts.KindHTMLDeep: deep}))
	if !hasFindingContaining(result2.Findings, "invisible without JavaScript") {
		t.Errorf("CSR page not flagged; findings: %+v", result2.Findings)
	}
	if result2.Score.Ratio() != 0 {
		t.Errorf("parity ratio = %v, want 0 (1 assessed page, failing)", result2.Score.Ratio())
	}
}
