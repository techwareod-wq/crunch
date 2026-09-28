package engine

import (
	"sort"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
)

// The four fixed remediation phases (decision 17: severity → phases in code,
// no LLM involved in bucketing).
var phaseTitles = [4]string{
	"Phase 1 — Critical fixes",
	"Phase 2 — High-impact improvements",
	"Phase 3 — Medium priority",
	"Phase 4 — Low priority & polish",
}

// quickWinChecks are check families whose findings are typically low-effort,
// same-day changes — sorted to the top of their phase so momentum starts
// where effort is cheapest.
var quickWinChecks = map[core.CheckID]bool{
	"onpage.titles":              true,
	"onpage.metas":               true,
	"onpage.headings":            true,
	"onpage.head_hygiene":        true,
	"images.alt_semantics":       true,
	"images.sizing":              true,
	"technical.sitemap":          true,
	"technical.indexnow":         true,
	"technical.canonicalization": true,
	"technical.site_files":       true,
	"aisearch.crawler_access":    true,
}

// Effort/owner labels per check (quality uplift 5.2 — static maps, ratified
// label sets: effort "30 min"/"hours"/"1–2 days"/"ongoing", owner
// Engineering/Content/Design/Founder-Growth). Unmapped checks fall back to
// "hours"/"Engineering"; LLM-judgment items to "ongoing"/"Content".
var effortByCheck = map[core.CheckID]string{
	"technical.broken_links":          "hours",
	"technical.redirects_status":      "hours",
	"technical.canonicalization":      "30 min",
	"technical.indexability_robots":   "30 min",
	"technical.https":                 "hours",
	"technical.sitemap":               "30 min",
	"technical.indexnow":              "30 min",
	"technical.security_headers":      "30 min",
	"technical.soft_404":              "hours",
	"technical.site_files":            "30 min",
	"onpage.titles":                   "hours",
	"onpage.metas":                    "hours",
	"onpage.headings":                 "hours",
	"onpage.duplicate_content":        "1–2 days",
	"onpage.sxo_mismatch":             "1–2 days",
	"onpage.programmatic_gates":       "1–2 days",
	"onpage.head_hygiene":             "30 min",
	"content.readability":             "ongoing",
	"content.substance":               "1–2 days",
	"content.keyword_density":         "hours",
	"content.internal_linking":        "1–2 days",
	"content.freshness":               "ongoing",
	"content.trust_signals":           "1–2 days",
	"content.filler_ai_patterns":      "ongoing",
	"content.scaled_patterns":         "ongoing",
	"content.parasite_markers":        "1–2 days",
	"content.llm_judgment":            "ongoing",
	"schema.validity":                 "hours",
	"schema.coverage":                 "hours",
	"schema.deprecations":             "30 min",
	"performance.cwv":                 "1–2 days",
	"performance.speculation_bfcache": "hours",
	"performance.psi_opportunities":   "1–2 days",
	"aisearch.answer_blocks":          "ongoing",
	"aisearch.structure":              "hours",
	"aisearch.authority_proxies":      "hours",
	"aisearch.crawler_access":         "30 min",
	"aisearch.agent_ux":               "1–2 days",
	"aisearch.multimodal":             "ongoing",
	"aisearch.brand_footprint":        "ongoing",
	"aisearch.rendered_parity":        "1–2 days",
	"images.alt_semantics":            "hours",
	"images.sizing":                   "hours",
	"backlinks.domain_rating":         "ongoing",
	"backlinks.profile_summary":       "ongoing",
}

var ownerByCheck = map[core.CheckID]string{
	"technical.broken_links":          "Engineering",
	"technical.redirects_status":      "Engineering",
	"technical.canonicalization":      "Engineering",
	"technical.indexability_robots":   "Engineering",
	"technical.https":                 "Engineering",
	"technical.sitemap":               "Engineering",
	"technical.indexnow":              "Engineering",
	"technical.security_headers":      "Engineering",
	"technical.soft_404":              "Engineering",
	"technical.site_files":            "Engineering",
	"onpage.titles":                   "Content",
	"onpage.metas":                    "Content",
	"onpage.headings":                 "Content",
	"onpage.duplicate_content":        "Content",
	"onpage.sxo_mismatch":             "Content",
	"onpage.programmatic_gates":       "Content",
	"onpage.head_hygiene":             "Engineering",
	"content.readability":             "Content",
	"content.substance":               "Content",
	"content.keyword_density":         "Content",
	"content.internal_linking":        "Content",
	"content.freshness":               "Content",
	"content.trust_signals":           "Content",
	"content.filler_ai_patterns":      "Content",
	"content.scaled_patterns":         "Content",
	"content.parasite_markers":        "Founder/Growth",
	"content.llm_judgment":            "Content",
	"schema.validity":                 "Engineering",
	"schema.coverage":                 "Engineering",
	"schema.deprecations":             "Engineering",
	"performance.cwv":                 "Engineering",
	"performance.speculation_bfcache": "Engineering",
	"performance.psi_opportunities":   "Engineering",
	"aisearch.answer_blocks":          "Content",
	"aisearch.structure":              "Content",
	"aisearch.authority_proxies":      "Engineering",
	"aisearch.crawler_access":         "Engineering",
	"aisearch.agent_ux":               "Design",
	"aisearch.multimodal":             "Design",
	"aisearch.brand_footprint":        "Founder/Growth",
	"aisearch.rendered_parity":        "Engineering",
	"images.alt_semantics":            "Content",
	"images.sizing":                   "Design",
	"backlinks.domain_rating":         "Founder/Growth",
	"backlinks.profile_summary":       "Founder/Growth",
}

// effortFor / ownerFor resolve the labels with the ratified defaults.
func effortFor(id core.CheckID) string {
	if e, ok := effortByCheck[id]; ok {
		return e
	}
	if id == "content.llm_judgment" {
		return "ongoing"
	}
	return "hours"
}

func ownerFor(id core.CheckID) string {
	if o, ok := ownerByCheck[id]; ok {
		return o
	}
	if id == "content.llm_judgment" {
		return "Content"
	}
	return "Engineering"
}

// BuildActionPlan buckets every finding into the four fixed phases by
// severity (critical → 1, high → 2, medium → 3, low/info → 4) and sorts
// quick wins first within each phase. Pure code — the narrative LLM call is
// layered on by the synthesize stage and is allowed to fail without touching
// this structure.
func BuildActionPlan(findings []core.Finding) models.ActionPlan {
	phases := make([]models.ActionPlanPhase, 4)
	for i := range phases {
		phases[i] = models.ActionPlanPhase{Title: phaseTitles[i]}
	}

	for _, f := range findings {
		// Info findings are context ("no action required", "not scored"), not
		// actions — an action plan listing things not to do is noise.
		if f.Severity == core.SeverityInfo {
			continue
		}
		idx := 3
		switch f.Severity {
		case core.SeverityCritical:
			idx = 0
		case core.SeverityHigh:
			idx = 1
		case core.SeverityMedium:
			idx = 2
		}
		phases[idx].Items = append(phases[idx].Items, models.ActionPlanItem{
			Finding:  f,
			QuickWin: quickWinChecks[f.CheckID],
			Effort:   effortFor(f.CheckID),
			Owner:    ownerFor(f.CheckID),
		})
	}

	for i := range phases {
		items := phases[i].Items
		sort.SliceStable(items, func(a, b int) bool {
			if items[a].QuickWin != items[b].QuickWin {
				return items[a].QuickWin
			}
			return core.SeverityRank(items[a].Finding.Severity) < core.SeverityRank(items[b].Finding.Severity)
		})
	}

	// Empty phases render as bare headers ("Phase 1 — Critical fixes" with
	// nothing under it reads like a bug, or worse, like withheld findings).
	var populated []models.ActionPlanPhase
	for _, ph := range phases {
		if len(ph.Items) > 0 {
			populated = append(populated, ph)
		}
	}
	return models.ActionPlan{Phases: populated}
}

// TopFindings returns the most severe findings (stable order) for the
// narrative call — the LLM reads only the top of the list, never the whole
// report.
func TopFindings(findings []core.Finding, max int) []core.Finding {
	sorted := make([]core.Finding, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(a, b int) bool {
		return core.SeverityRank(sorted[a].Severity) < core.SeverityRank(sorted[b].Severity)
	})
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	return sorted
}
