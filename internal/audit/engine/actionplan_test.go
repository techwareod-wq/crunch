package engine

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

func TestBuildActionPlan_SeverityBucketing(t *testing.T) {
	findings := []core.Finding{
		{CheckID: "technical.https", Severity: core.SeverityCritical, Title: "c"},
		{CheckID: "content.substance", Severity: core.SeverityHigh, Title: "h"},
		{CheckID: "onpage.metas", Severity: core.SeverityMedium, Title: "m"},
		{CheckID: "content.freshness", Severity: core.SeverityLow, Title: "l"},
		{CheckID: "aisearch.crawler_access", Severity: core.SeverityInfo, Title: "i"},
	}
	plan := BuildActionPlan(findings)
	if len(plan.Phases) != 4 {
		t.Fatalf("phases = %d, want 4 populated phases", len(plan.Phases))
	}
	// Round 4: Info findings are context, not actions — excluded from the plan.
	wantCounts := []int{1, 1, 1, 1}
	for i, want := range wantCounts {
		if got := len(plan.Phases[i].Items); got != want {
			t.Errorf("phase %d items = %d, want %d", i+1, got, want)
		}
	}
	if plan.Narrative != nil {
		t.Error("code bucketing must not set a narrative — that's the LLM layer's field")
	}

	// Round 4: phases with no items are pruned — a bare "Critical fixes"
	// header with nothing under it reads as withheld findings.
	onlyLow := BuildActionPlan([]core.Finding{{CheckID: "content.freshness", Severity: core.SeverityLow, Title: "l"}})
	if len(onlyLow.Phases) != 1 || onlyLow.Phases[0].Title != phaseTitles[3] {
		t.Errorf("empty phases must be pruned; got %+v", onlyLow.Phases)
	}
}

// TestBuildActionPlan_QuickWinsFirst: within a phase, quick wins sort ahead.
func TestBuildActionPlan_QuickWinsFirst(t *testing.T) {
	findings := []core.Finding{
		{CheckID: "content.substance", Severity: core.SeverityMedium, Title: "slow work"},
		{CheckID: "onpage.metas", Severity: core.SeverityMedium, Title: "quick win"},
	}
	plan := BuildActionPlan(findings)
	if len(plan.Phases) != 1 {
		t.Fatalf("phases = %d, want 1 (medium only; empty phases pruned)", len(plan.Phases))
	}
	items := plan.Phases[0].Items
	if len(items) != 2 {
		t.Fatalf("phase items = %d, want 2", len(items))
	}
	if !items[0].QuickWin || items[0].Finding.CheckID != "onpage.metas" {
		t.Errorf("quick win not sorted first: %+v", items[0])
	}
}

func TestTopFindings(t *testing.T) {
	findings := []core.Finding{
		{Severity: core.SeverityLow, Title: "l"},
		{Severity: core.SeverityCritical, Title: "c"},
		{Severity: core.SeverityMedium, Title: "m"},
	}
	top := TopFindings(findings, 2)
	if len(top) != 2 || top[0].Severity != core.SeverityCritical || top[1].Severity != core.SeverityMedium {
		t.Errorf("TopFindings = %+v, want critical then medium", top)
	}
	// Input order untouched (copy semantics).
	if findings[0].Severity != core.SeverityLow {
		t.Error("TopFindings mutated its input")
	}
}
