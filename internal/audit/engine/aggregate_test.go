package engine

import (
	"reflect"
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
)

func v1Snapshot(findingsOnly ...core.CheckID) spec.Snapshot {
	return spec.SpecV1().Snapshot(findingsOnly)
}

// TestAggregate_Renormalization: a skipped check's allocation must
// renormalize away within its category, not count against the site.
func TestAggregate_Renormalization(t *testing.T) {
	snap := v1Snapshot()
	inputs := []AggregateInput{
		// technical: broken_links (alloc 20) earns 50%, sitemap (15) skipped,
		// everything else in the category absent from inputs entirely.
		{CheckID: "technical.broken_links", Category: core.CategoryTechnical, HasScore: true, Earned: 50, Possible: 100},
		{CheckID: "technical.sitemap", Category: core.CategoryTechnical, Skipped: true},
	}
	result := Aggregate(inputs, snap)
	tech := findCategory(t, result, core.CategoryTechnical)
	if !tech.Scored || tech.Score != 50 {
		t.Errorf("technical = %+v, want scored 50 (only broken_links feeds; its ratio IS the category)", tech)
	}
}

// TestAggregate_FindingsOnlyDemotion: a values-demoted check must not feed
// its category even though it produced a score.
func TestAggregate_FindingsOnlyDemotion(t *testing.T) {
	snap := v1Snapshot("technical.broken_links")
	inputs := []AggregateInput{
		{CheckID: "technical.broken_links", Category: core.CategoryTechnical, HasScore: true, Earned: 0, Possible: 100},
		{CheckID: "technical.https", Category: core.CategoryTechnical, HasScore: true, Earned: 100, Possible: 100},
	}
	result := Aggregate(inputs, snap)
	tech := findCategory(t, result, core.CategoryTechnical)
	if !tech.Scored || tech.Score != 100 {
		t.Errorf("technical = %+v, want 100 (demoted zero must not drag the category)", tech)
	}
}

// TestAggregate_UnscoredCategoryExcludedFromOverall: the honesty rule — a
// category with no measurable checks is named unscored and excluded, never
// silently zeroed.
func TestAggregate_UnscoredCategoryExcludedFromOverall(t *testing.T) {
	snap := v1Snapshot()
	inputs := []AggregateInput{
		{CheckID: "technical.https", Category: core.CategoryTechnical, HasScore: true, Earned: 80, Possible: 100},
		{CheckID: "performance.cwv", Category: core.CategoryPerformance, Skipped: true},
	}
	result := Aggregate(inputs, snap)

	perf := findCategory(t, result, core.CategoryPerformance)
	if perf.Scored || perf.Reason == "" {
		t.Errorf("performance = %+v, want scored:false with a machine-readable reason", perf)
	}
	// Overall over scored categories only: only technical (80) fed it.
	if result.Overall != 80 {
		t.Errorf("overall = %d, want 80 (performance excluded, not zeroed)", result.Overall)
	}
	found := false
	for _, id := range result.Unscored {
		if id == core.CategoryPerformance {
			found = true
		}
	}
	if !found {
		t.Error("performance missing from Unscored — the exclusion must be named")
	}
}

// TestAggregate_WeightedOverall: overall = Σ score×weight / Σweight over
// scored categories.
func TestAggregate_WeightedOverall(t *testing.T) {
	snap := v1Snapshot()
	inputs := []AggregateInput{
		{CheckID: "technical.https", Category: core.CategoryTechnical, HasScore: true, Earned: 100, Possible: 100},        // w20 → 100
		{CheckID: "backlinks.domain_rating", Category: core.CategoryBacklinks, HasScore: true, Earned: 40, Possible: 100}, // w10 → 40
	}
	result := Aggregate(inputs, snap)
	// (100×20 + 40×10) / 30 = 80.
	if result.Overall != 80 {
		t.Errorf("overall = %d, want 80", result.Overall)
	}
}

// TestAggregate_Determinism: same inputs + same snapshot ⇒ identical result
// (what the V2 drift/compare feature depends on).
func TestAggregate_Determinism(t *testing.T) {
	snap := v1Snapshot()
	inputs := []AggregateInput{
		{CheckID: "technical.https", Category: core.CategoryTechnical, HasScore: true, Earned: 73, Possible: 100},
		{CheckID: "onpage.titles", Category: core.CategoryOnPage, HasScore: true, Earned: 61.5, Possible: 100},
		{CheckID: "performance.cwv", Category: core.CategoryPerformance, Skipped: true},
	}
	first := Aggregate(inputs, snap)
	second := Aggregate(inputs, snap)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("aggregation is nondeterministic:\n%+v\n%+v", first, second)
	}
}

func findCategory(t *testing.T, result AggregateResult, id core.CategoryID) CategoryScore {
	t.Helper()
	for _, c := range result.Categories {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("category %s missing from result", id)
	return CategoryScore{}
}
