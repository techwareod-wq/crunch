package engine

import (
	"math"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
)

// CategoryScore is one category's aggregation result.
type CategoryScore struct {
	ID     core.CategoryID
	Scored bool
	Score  int
	// Reason is machine-readable when Scored is false.
	Reason string
}

// AggregateInput is one check's contribution to aggregation (a slimmed view
// over the persisted outcomes, so aggregation is testable without models).
type AggregateInput struct {
	CheckID  core.CheckID
	Category core.CategoryID
	Skipped  bool
	HasScore bool
	Earned   float64
	Possible float64
}

// AggregateResult is the §7 math output.
type AggregateResult struct {
	Categories []CategoryScore
	Overall    int
	// Unscored names the categories excluded from the overall — the honesty
	// rule (never silently score a category we couldn't measure).
	Unscored []core.CategoryID
}

// Aggregate implements the §7 scoring math over a run's outcomes + frozen
// snapshot:
//
//  1. A category's effective set = its checks that RAN and are score-feeding
//     (contribute a Score, hold a snapshot allocation, and are not demoted to
//     findings-only).
//  2. categoryScore = round(Σ(ratio_i × A_i) / ΣA_i × 100) — allocations of
//     skipped/demoted checks renormalize away within the category.
//  3. Empty effective set ⇒ scored:false + reason.
//  4. overall = round(Σ categoryScore × W / ΣW) over scored categories only.
//
// Category order follows the snapshot's spec version when known (stable
// report ordering), falling back to first-seen order.
func Aggregate(inputs []AggregateInput, snapshot spec.Snapshot) AggregateResult {
	type catAgg struct {
		weighted float64 // Σ ratio×alloc
		alloc    float64 // Σ alloc
		anyRan   bool
	}
	agg := map[core.CategoryID]*catAgg{}
	ensure := func(id core.CategoryID) *catAgg {
		if agg[id] == nil {
			agg[id] = &catAgg{}
		}
		return agg[id]
	}

	for _, in := range inputs {
		a := ensure(in.Category)
		if in.Skipped {
			continue
		}
		a.anyRan = true
		alloc, hasAlloc := snapshot.Allocations[in.CheckID]
		if !hasAlloc || !in.HasScore || snapshot.IsFindingsOnly(in.CheckID) {
			continue
		}
		ratio := core.Score{Earned: in.Earned, Possible: in.Possible}.Ratio()
		a.weighted += ratio * float64(alloc)
		a.alloc += float64(alloc)
	}

	result := AggregateResult{}
	var weightedSum, weightTotal float64
	for _, id := range categoryOrder(snapshot) {
		a := agg[id]
		weight := snapshot.Weights[id]
		if a == nil || a.alloc == 0 {
			reason := "no_measurable_checks"
			if a != nil && a.anyRan {
				reason = "no_score_feeding_checks"
			}
			result.Categories = append(result.Categories, CategoryScore{ID: id, Scored: false, Reason: reason})
			result.Unscored = append(result.Unscored, id)
			continue
		}
		score := int(math.Round(a.weighted / a.alloc * 100))
		result.Categories = append(result.Categories, CategoryScore{ID: id, Scored: true, Score: score})
		weightedSum += float64(score) * float64(weight)
		weightTotal += float64(weight)
	}

	if weightTotal > 0 {
		result.Overall = int(math.Round(weightedSum / weightTotal))
	}
	return result
}

// categoryOrder yields the snapshot's categories in the spec literal's
// declared order when the version is known, else sorted-stable over the
// weight map (deterministic either way).
func categoryOrder(snapshot spec.Snapshot) []core.CategoryID {
	if sp, err := spec.ByVersion(snapshot.Version); err == nil {
		var out []core.CategoryID
		for _, c := range sp.Categories {
			if _, ok := snapshot.Weights[c.ID]; ok {
				out = append(out, c.ID)
			}
		}
		// A snapshot may carry categories the literal no longer declares —
		// append them so nothing silently drops.
		seen := map[core.CategoryID]bool{}
		for _, id := range out {
			seen[id] = true
		}
		for id := range snapshot.Weights {
			if !seen[id] {
				out = append(out, id)
			}
		}
		if len(out) > 1 {
			stableSortTail(out, len(sp.Categories))
		}
		return out
	}
	var out []core.CategoryID
	for id := range snapshot.Weights {
		out = append(out, id)
	}
	sortCategoryIDs(out)
	return out
}

func stableSortTail(ids []core.CategoryID, declared int) {
	if declared < len(ids) {
		tail := ids[declared:]
		sortCategoryIDs(tail)
	}
}

func sortCategoryIDs(ids []core.CategoryID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// CategoryLabel resolves a display label from the snapshot's spec version,
// falling back to the raw id for unknown versions.
func CategoryLabel(snapshot spec.Snapshot, id core.CategoryID) string {
	if sp, err := spec.ByVersion(snapshot.Version); err == nil {
		for _, c := range sp.Categories {
			if c.ID == id {
				return c.Label
			}
		}
	}
	return string(id)
}
