package spec

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

// TestSpecV1_Weights locks the decision-13 numbers: any drift here is a
// SpecV2, never an edit.
func TestSpecV1_Weights(t *testing.T) {
	s := SpecV1()
	want := map[core.CategoryID]int{
		core.CategoryTechnical:   20,
		core.CategoryContentEEAT: 21,
		core.CategoryOnPage:      18,
		core.CategorySchema:      9,
		core.CategoryPerformance: 9,
		core.CategoryAISearch:    9,
		core.CategoryImages:      4,
		core.CategoryBacklinks:   10,
	}
	if len(s.Categories) != len(want) {
		t.Fatalf("SpecV1 has %d categories, want %d", len(s.Categories), len(want))
	}
	total := 0
	for _, c := range s.Categories {
		if want[c.ID] != c.Weight {
			t.Errorf("category %s weight = %d, want %d", c.ID, c.Weight, want[c.ID])
		}
		total += c.Weight
	}
	if total != 100 {
		t.Errorf("weights sum to %d, want 100", total)
	}
}

// TestSpecV1_ContentJudgmentAllocation locks the LLM check's 50-point slice
// and parasite_markers' intentional absence (intrinsically findings-only).
func TestSpecV1_ContentJudgmentAllocation(t *testing.T) {
	s := SpecV1()
	if got := s.Allocations["content.llm_judgment"]; got != 50 {
		t.Errorf("content.llm_judgment allocation = %d, want 50", got)
	}
	if _, ok := s.Allocations["content.parasite_markers"]; ok {
		t.Error("content.parasite_markers must carry no allocation (findings-only by nature)")
	}
}

func TestByVersion(t *testing.T) {
	if _, err := ByVersion("v1"); err != nil {
		t.Fatalf("ByVersion(v1): %v", err)
	}
	if _, err := ByVersion(""); err != nil {
		t.Fatalf("ByVersion(empty → default v1): %v", err)
	}
	if _, err := ByVersion("v99"); err == nil {
		t.Fatal("ByVersion(v99) should fail")
	}
}

// TestSpecV1_RatifiedUpliftTable locks the quality-uplift allocations
// (RATIFIED 2026-08-30, folded into V1 pre-launch by user decision): the
// eight new checks allocated exactly as ratified, findings-only checks
// absent. Any drift from here is a SpecV2.
func TestSpecV1_RatifiedUpliftTable(t *testing.T) {
	s := SpecV1()
	want := map[core.CheckID]int{
		"technical.broken_links": 18, "technical.redirects_status": 12,
		"technical.canonicalization": 14, "technical.indexability_robots": 18,
		"technical.https": 8, "technical.sitemap": 12, "technical.indexnow": 4,
		"technical.security_headers": 8, "technical.soft_404": 6,
		"onpage.titles": 22, "onpage.metas": 18, "onpage.headings": 15,
		"onpage.duplicate_content": 15, "onpage.sxo_mismatch": 15,
		"onpage.programmatic_gates": 10, "onpage.head_hygiene": 5,
		"content.readability": 7, "content.substance": 7, "content.keyword_density": 4,
		"content.internal_linking": 8, "content.freshness": 4, "content.trust_signals": 8,
		"content.filler_ai_patterns": 8, "content.scaled_patterns": 4, "content.llm_judgment": 50,
		"schema.validity": 40, "schema.coverage": 35, "schema.deprecations": 25,
		"aisearch.answer_blocks": 20, "aisearch.structure": 15, "aisearch.authority_proxies": 15,
		"aisearch.crawler_access": 10, "aisearch.agent_ux": 5, "aisearch.multimodal": 10,
		"aisearch.brand_footprint": 15, "aisearch.rendered_parity": 10,
	}
	for id, alloc := range want {
		if got := s.Allocations[id]; got != alloc {
			t.Errorf("SpecV1 allocation %s = %d, want %d", id, got, alloc)
		}
	}
	for _, findingsOnly := range []core.CheckID{"content.parasite_markers", "technical.site_files"} {
		if _, ok := s.Allocations[findingsOnly]; ok {
			t.Errorf("%s must carry no allocation (findings-only by nature)", findingsOnly)
		}
	}
}

// TestSnapshot_RoundTrip: the snapshot must be self-describing — weights,
// allocations, and the demotion set all frozen.
func TestSnapshot_RoundTrip(t *testing.T) {
	s := SpecV1()
	snap := s.Snapshot([]core.CheckID{"onpage.titles"})
	if snap.Version != "v1" {
		t.Errorf("snapshot version = %q", snap.Version)
	}
	if snap.Weights[core.CategoryBacklinks] != 10 {
		t.Errorf("snapshot backlinks weight = %d, want 10", snap.Weights[core.CategoryBacklinks])
	}
	if snap.Allocations["technical.broken_links"] != 18 {
		t.Errorf("snapshot allocation = %d, want 18", snap.Allocations["technical.broken_links"])
	}
	if !snap.IsFindingsOnly("onpage.titles") {
		t.Error("snapshot lost the findings-only demotion")
	}
	if snap.IsFindingsOnly("onpage.metas") {
		t.Error("snapshot demoted a check it shouldn't have")
	}
}
