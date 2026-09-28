package analytics

import (
	"github.com/atharva-ng/crunch/internal/models"
)

// Collision merge (LLD §3.1a). Normalization can map several source rows onto
// one fact identity — GSC reports http:// and https:// variants of a page as
// separate rows that normalize to the same (grain, date, DimKey), and chunked
// raw docs can split one page's rows. A naive upsert would let the last row
// win and UNDERCOUNT, so the orchestrator folds every run's facts through the
// source's MergePolicy before the bulk upsert. Replay uses the same path, so
// merges are identical on rebuild.

// MergeKind selects how one metric folds across collided facts.
type MergeKind string

const (
	// MergeKindSum adds the values — counts (clicks, impressions). The
	// default for any metric missing from the policy map.
	MergeKindSum MergeKind = "sum"
	// MergeKindWeighted is the WeightBy-weighted mean — position weights by
	// impressions. Zero total weight falls back to the unweighted mean.
	MergeKindWeighted MergeKind = "weighted"
	// MergeKindRecompute derives the value from two already-merged metrics
	// AFTER the fold — ctr = merged clicks / merged impressions.
	MergeKindRecompute MergeKind = "recompute"
	// MergeKindLast keeps the last row's value — non-additive gauges.
	MergeKindLast MergeKind = "last"
)

// MergeOp is one metric's collision rule.
type MergeOp struct {
	Kind MergeKind
	// WeightBy names the metric whose per-row values weight a Weighted mean.
	WeightBy string
	// Numerator/Denominator name the MERGED metrics a Recompute divides.
	Numerator, Denominator string
}

// MergeSum, MergeWeighted, MergeRecompute, MergeLast build the policy entries
// source packages declare.
func MergeSum() MergeOp { return MergeOp{Kind: MergeKindSum} }
func MergeWeighted(weightBy string) MergeOp {
	return MergeOp{Kind: MergeKindWeighted, WeightBy: weightBy}
}
func MergeRecompute(numerator, denominator string) MergeOp {
	return MergeOp{Kind: MergeKindRecompute, Numerator: numerator, Denominator: denominator}
}
func MergeLast() MergeOp { return MergeOp{Kind: MergeKindLast} }

// mergeFacts folds facts sharing a (grain, date, DimKey) identity into one,
// preserving first-seen order. Non-collided facts pass through untouched —
// their source-reported values (including ratios like ctr) are kept verbatim.
func mergeFacts(facts []models.AnalyticsFact, policy map[string]MergeOp) []models.AnalyticsFact {
	type groupKey struct{ grain, date, dimKey string }
	groups := map[groupKey][]models.AnalyticsFact{}
	order := make([]groupKey, 0, len(facts))
	for _, f := range facts {
		k := groupKey{f.Grain, f.Date, models.AnalyticsDimKey(f.Dims)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], f)
	}

	merged := make([]models.AnalyticsFact, 0, len(order))
	for _, k := range order {
		group := groups[k]
		if len(group) == 1 {
			merged = append(merged, group[0])
			continue
		}
		merged = append(merged, foldGroup(group, policy))
	}
	return merged
}

// foldGroup merges one collided identity. The identity fields (and Dims — the
// DimKey encoding is injective, so equal keys mean equal maps) come from the
// first row.
func foldGroup(group []models.AnalyticsFact, policy map[string]MergeOp) models.AnalyticsFact {
	out := group[0]
	out.Metrics = map[string]float64{}

	// Collect every metric key present in the group.
	keys := map[string]bool{}
	for _, f := range group {
		for m := range f.Metrics {
			keys[m] = true
		}
	}

	recomputes := map[string]MergeOp{}
	for m := range keys {
		op, ok := policy[m]
		if !ok {
			op = MergeSum()
		}
		switch op.Kind {
		case MergeKindRecompute:
			recomputes[m] = op // needs the merged inputs; second pass
		case MergeKindLast:
			for _, f := range group {
				if v, ok := f.Metrics[m]; ok {
					out.Metrics[m] = v
				}
			}
		case MergeKindWeighted:
			var weightedSum, weightSum, plainSum float64
			var n int
			for _, f := range group {
				v, ok := f.Metrics[m]
				if !ok {
					continue
				}
				w := f.Metrics[op.WeightBy]
				weightedSum += v * w
				weightSum += w
				plainSum += v
				n++
			}
			if weightSum > 0 {
				out.Metrics[m] = weightedSum / weightSum
			} else if n > 0 {
				out.Metrics[m] = plainSum / float64(n)
			}
		default: // MergeKindSum
			var sum float64
			for _, f := range group {
				sum += f.Metrics[m]
			}
			out.Metrics[m] = sum
		}
	}

	for m, op := range recomputes {
		if den := out.Metrics[op.Denominator]; den > 0 {
			out.Metrics[m] = out.Metrics[op.Numerator] / den
		} else {
			out.Metrics[m] = 0
		}
	}
	return out
}
