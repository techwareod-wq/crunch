package spec

import "github.com/atharva-ng/crunch/internal/audit/core"

// SpecV1 is the launch scoring contract: the 2026-08-30 quality-uplift table
// (SEO_AUDIT_QUALITY_UPLIFT_PLAN.md Part 6, ratified same day) folded into V1
// pre-launch by user decision — the engine has never shipped, so V1 launches
// WITH the eight new checks rather than carrying a dead pre-uplift version.
// The never-edit rule applies from first release onward: any rebalancing from
// here is a SpecV2 literal.
//
// Category weights (sum 100): technical 20 / content_eeat 21 / on_page 18 /
// schema 9 / performance 9 / ai_search 9 / images 4 / backlinks 10 — the ~10%
// proportional carve from the reference weights to fit Backlinks in.
//
// Findings-only by nature (no allocation): content.parasite_markers,
// technical.site_files.
func SpecV1() Spec {
	s := Spec{
		Version: "v1",
		Categories: []Category{
			{ID: core.CategoryTechnical, Label: "Technical", Weight: 20},
			{ID: core.CategoryContentEEAT, Label: "Content & E-E-A-T", Weight: 21},
			{ID: core.CategoryOnPage, Label: "On-Page", Weight: 18},
			{ID: core.CategorySchema, Label: "Schema", Weight: 9},
			{ID: core.CategoryPerformance, Label: "Performance", Weight: 9},
			{ID: core.CategoryAISearch, Label: "AI Search", Weight: 9},
			{ID: core.CategoryImages, Label: "Images", Weight: 4},
			{ID: core.CategoryBacklinks, Label: "Backlinks", Weight: 10},
		},
		Allocations: map[core.CheckID]int{
			// technical (sums 100)
			"technical.broken_links":        18,
			"technical.redirects_status":    12,
			"technical.canonicalization":    14,
			"technical.indexability_robots": 18,
			"technical.https":               8,
			"technical.sitemap":             12,
			"technical.indexnow":            4,
			"technical.security_headers":    8,
			"technical.soft_404":            6,

			// on_page (sums 100)
			"onpage.titles":             22,
			"onpage.metas":              18,
			"onpage.headings":           15,
			"onpage.duplicate_content":  15,
			"onpage.sxo_mismatch":       15,
			"onpage.programmatic_gates": 10,
			"onpage.head_hygiene":       5,

			// content_eeat (sums 100; internal_linking's 8 reflects the
			// external audit treating orphaned content as P0-critical)
			"content.readability":        7,
			"content.substance":          7,
			"content.keyword_density":    4,
			"content.internal_linking":   8,
			"content.freshness":          4,
			"content.trust_signals":      8,
			"content.filler_ai_patterns": 8,
			"content.scaled_patterns":    4,
			"content.llm_judgment":       50,

			// schema (sums 100)
			"schema.validity":     40,
			"schema.coverage":     35,
			"schema.deprecations": 25,

			// performance (sums 100)
			"performance.cwv":                 70,
			"performance.speculation_bfcache": 15,
			"performance.psi_opportunities":   15,

			// ai_search (sums 100)
			"aisearch.answer_blocks":     20,
			"aisearch.structure":         15,
			"aisearch.authority_proxies": 15,
			"aisearch.crawler_access":    10,
			"aisearch.agent_ux":          5,
			"aisearch.multimodal":        10,
			"aisearch.brand_footprint":   15,
			"aisearch.rendered_parity":   10,

			// images (sums 100)
			"images.alt_semantics": 60,
			"images.sizing":        40,

			// backlinks (sums 100)
			"backlinks.domain_rating":   60,
			"backlinks.profile_summary": 40,
		},
	}
	if err := validate(s); err != nil {
		// A broken literal is a programming error caught by the boot path and
		// the spec tests — panic keeps the constructor signature clean.
		panic(err)
	}
	return s
}
