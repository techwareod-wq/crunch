package engine

import (
	"fmt"
	"sort"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
	"github.com/atharva-ng/crunch/internal/models"
)

// evidenceNumber reads a numeric evidence value regardless of whether it
// arrives fresh from a check (float64/int) or round-tripped through BSON
// (float64/int32/int64). 0 = absent or non-numeric.
func evidenceNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// maxStrengths caps the "what's already right" list — enough to build trust,
// not a second report.
const maxStrengths = 10

// strengthBlurbs maps a check that scored ≥95% with no Medium+ findings onto
// its static strength line (quality uplift 5.1 — pure code, no LLM).
var strengthBlurbs = map[core.CheckID]string{
	"technical.broken_links":          "No broken internal links — every crawled link resolves",
	"technical.redirects_status":      "Clean internal routing — no error pages or redirect hops in the link graph",
	"technical.canonicalization":      "Canonical tags are consistent and point at live, final URLs",
	"technical.indexability_robots":   "Indexation is clean — no stray noindex directives on linked pages",
	"technical.https":                 "Fully served over valid HTTPS with no mixed content",
	"technical.sitemap":               "The XML sitemap is present, populated, and carries lastmod dates",
	"technical.indexnow":              "IndexNow is set up — new content gets pushed to search engines instantly",
	"technical.security_headers":      "Security headers are in place (HSTS, CSP, content-type protection)",
	"technical.soft_404":              "Nonexistent URLs return real 404s — no soft-404 crawl-budget leak",
	"onpage.titles":                   "Titles are unique and well-sized across the crawled pages",
	"onpage.metas":                    "Meta descriptions are unique and in the effective length band",
	"onpage.headings":                 "Crawled pages carry exactly one clean H1 each",
	"onpage.duplicate_content":        "No near-duplicate content among the crawled pages",
	"onpage.sxo_mismatch":             "Page formats match what actually ranks for their keywords",
	"onpage.programmatic_gates":       "No programmatic/doorway footprint — pages are individually substantive",
	"onpage.head_hygiene":             "Social meta (Open Graph, Twitter cards) and language tags are complete",
	"content.readability":             "Sampled content reads clearly — sentence and paragraph lengths in the healthy band",
	"content.substance":               "Sampled pages carry real substance — no thin content in the sample",
	"content.keyword_density":         "Natural keyword usage — no stuffing patterns",
	"content.internal_linking":        "Content pages cross-link contextually — link equity flows between articles",
	"content.freshness":               "Content is dated and recently maintained",
	"content.trust_signals":           "Strong trust signals: bylines, About/Contact, and real citations",
	"content.filler_ai_patterns":      "Copy is free of filler and stock AI phrasing",
	"content.scaled_patterns":         "Publishing cadence and title variety look organic — no scaled-content footprint",
	"schema.validity":                 "Structured data parses cleanly across the sample",
	"schema.coverage":                 "Schema coverage is right-side-up: entity markup on the homepage, Article markup on posts",
	"schema.deprecations":             "No schema types targeting retired rich results",
	"performance.cwv":                 "Core Web Vitals pass at the 75th percentile — real-user speed is healthy",
	"performance.speculation_bfcache": "Navigation is optimized — no bfcache blockers detected",
	"performance.psi_opportunities":   "Lighthouse finds no significant performance savings left on the table",
	"aisearch.answer_blocks":          "Articles open with citable, self-contained answer blocks",
	"aisearch.structure":              "Content is structured for extraction — clear headings including question forms",
	"aisearch.authority_proxies":      "Entity signals are declared: Organization schema, sameAs links, author bylines",
	"aisearch.crawler_access":         "AI crawlers are allowed — the site is visible to answer engines",
	"aisearch.agent_ux":               "Semantic, landmark-rich markup — easy for agents and assistive tech to operate",
	"aisearch.multimodal":             "Content mixes modalities — images, lists/tables, or video alongside prose",
	"aisearch.brand_footprint":        "A real third-party footprint exists — AI engines have sources to cite",
	"aisearch.rendered_parity":        "Content is fully present without JavaScript — AI crawlers see the real page",
	"images.alt_semantics":            "Sampled images carry descriptive alt text",
	"images.sizing":                   "Images are declared and sized appropriately for their placement",
	"backlinks.domain_rating":         "Domain authority is strong for the niche",
	"backlinks.profile_summary":       "The backlink profile is healthy — diverse referring domains, low breakage",
}

// strengthThreshold: a check contributes a strength at ≥95% of its possible
// score with zero Medium-or-worse findings.
const strengthThreshold = 0.95

// evidenceBool reads a boolean evidence value fresh or BSON-round-tripped.
func evidenceBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// BuildStrengths derives the "what's already right" section from the run's
// outcomes (quality uplift 5.1): near-perfect, finding-clean checks emit
// their static blurb, ordered by category weight (then category, then check
// id — deterministic), capped at maxStrengths.
func BuildStrengths(outcomes []models.AuditCheckOutcome, snapshot spec.Snapshot) []models.StrengthItem {
	var items []models.StrengthItem
	contributed := map[string]bool{}
	for _, o := range outcomes {
		if o.Skipped || !o.HasScore || o.Possible <= 0 {
			continue
		}
		if o.Earned/o.Possible < strengthThreshold {
			continue
		}
		// A check can veto its own strength (gap-closure round 4): a perfect
		// score earned from a thin assessed base or without corroborating
		// evidence is absence of evidence, and the report must not praise it.
		if evidenceBool(o.Evidence["strengthIneligible"]) {
			continue
		}
		blurb, ok := strengthBlurbs[core.CheckID(o.CheckID)]
		if !ok {
			continue
		}
		// Any actionable finding (Low or worse) blocks the blurb — a report
		// that praises "no redirect hops" three lines above a "4 internally-
		// linked redirects" finding is contradicting itself. Info-only notes
		// don't block.
		actionable := false
		for _, f := range o.Findings {
			if core.SeverityRank(f.Severity) <= core.SeverityRank(core.SeverityLow) {
				actionable = true
				break
			}
		}
		if actionable {
			continue
		}
		contributed[o.CheckID] = true
		items = append(items, models.StrengthItem{
			CheckID:  o.CheckID,
			Category: o.Category,
			Text:     blurb,
		})
	}

	// llms.txt full coverage rides the crawler_access evidence rather than
	// its score (it carries zero points by design — fidelity must).
	for _, o := range outcomes {
		if o.CheckID != "aisearch.crawler_access" || o.Skipped {
			continue
		}
		if v, ok := o.Evidence["llmsTxt"].(string); ok && v == "covered" {
			items = append(items, models.StrengthItem{
				CheckID:  o.CheckID,
				Category: o.Category,
				Text:     "llms.txt is present and covers both the content and the company/product layer",
			})
		}
	}

	// A strong Lighthouse lab score rides psi_opportunities' evidence with
	// the actual number — real, quotable speed praise even when small
	// opportunities keep the check just under the blurb threshold. Never next
	// to a Medium+ savings finding, though.
	for _, o := range outcomes {
		if o.CheckID != "performance.psi_opportunities" || o.Skipped || contributed[o.CheckID] {
			continue
		}
		mediumPlus := false
		for _, f := range o.Findings {
			if core.SeverityRank(f.Severity) <= core.SeverityRank(core.SeverityMedium) {
				mediumPlus = true
				break
			}
		}
		if mediumPlus {
			continue
		}
		if lab := evidenceNumber(o.Evidence["labPerformanceScore"]); lab >= 90 {
			items = append(items, models.StrengthItem{
				CheckID:  o.CheckID,
				Category: o.Category,
				Text:     fmt.Sprintf("Lab performance is excellent — Lighthouse scores it %d/100 on mobile", int(lab)),
			})
		}
	}

	sort.SliceStable(items, func(a, b int) bool {
		wa := snapshot.Weights[core.CategoryID(items[a].Category)]
		wb := snapshot.Weights[core.CategoryID(items[b].Category)]
		if wa != wb {
			return wa > wb
		}
		if items[a].Category != items[b].Category {
			return items[a].Category < items[b].Category
		}
		return items[a].CheckID < items[b].CheckID
	})
	if len(items) > maxStrengths {
		items = items[:maxStrengths]
	}
	return items
}
