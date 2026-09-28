package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// jaccardDupeThreshold is the stemmed token-set similarity at which two
// keywords are considered near-duplicates and only the stronger one (by
// provisional opportunity score, protection trumping) survives.
const jaccardDupeThreshold = 0.85

// UpgradeExpand is the SIE_UPGRADE_EXPAND kickoff: after the user subscribes,
// re-run the ORIGINAL SIE pipeline in full mode on top of the trial data. It
// rewinds the WEC's pipeline cursor (flipping sie_mode to full so every stage
// picks the normal limits via limitsFor) and re-enters Orchestrate, which
// dispatches the standard async stages. The stages themselves are merge-aware:
//   - PostProcessing runs the merged variant dedupe (dedupeMergedKeywords)
//     over existing docs ∪ the full-limit pools: keywords with a scheduled
//     article and cluster pillars always survive, other stored keywords can
//     lose their variant group to a higher-scoring fresh variant (doc deleted
//     and pulled from cluster supporting lists) — never a wholesale wipe,
//   - funnel chunks skip keywords that already carry a funnel value,
//   - scoring re-scores the full set idempotently,
//   - clustering switches to merge semantics when clusters already exist,
//   - the post-clustering hand-off dispatches SE_EXTEND_SCHEDULE (append)
//     instead of SE Orchestrate, which finalizes upgrade_state=complete.
//
// NEVER deletes scheduled rows, clusters, or any keyword an article hangs off;
// the only deletions are article-less stored keywords replaced by a stronger
// variant. Idempotent: a redelivered kickoff loses the rewind CAS and just
// re-dispatches the incomplete stages (resume), and each stage is claim-guarded.
func (s *seoBlogGeneratorSiteIntelligence) UpgradeExpand(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	ok, wec, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("upgrade expand: get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("upgrade expand: web entity context not found: %s", metadata.WebEntityContextID)
	}

	// Already finalized (redelivery after completion, or a second activation
	// event racing the finalize) — nothing to do.
	if wec.UpgradeState == models.WECUpgradeStateComplete {
		return nil
	}
	// Never claimed: the message shouldn't exist without the webhook's CAS —
	// drop rather than re-run a WEC the webhook didn't claim.
	if wec.UpgradeState != models.WECUpgradeStateExpanding {
		log.Warn("upgrade expand: message for unclaimed WEC dropped",
			"webEntityContextId", metadata.WebEntityContextID, "upgradeState", wec.UpgradeState)
		return nil
	}

	// sie_mode still trial means the rewind hasn't happened yet.
	if wec.EffectiveSIEMode() == models.SIEModeTrial {
		// The base trial pipeline must be finished before re-running on top of
		// it. Returning an error lets SQS backoff retry until scheduling
		// completes.
		if wec.EffectiveStatus() < models.SIEStatusSchedulingDone {
			return fmt.Errorf("upgrade expand: base pipeline not finished (effective status %d) for WEC %s — retrying",
				wec.EffectiveStatus(), metadata.WebEntityContextID)
		}
		// Keep the trial run's pre-expanded seeds when present so the expansion
		// fetch reuses them; otherwise the GetPreExpanded stage regenerates.
		keepSeeds := len(wec.Keywords.PreExpandedKeyWords) > 0
		if _, err := models.RewindWECForUpgradeRerun(ctx, wec.ID, keepSeeds); err != nil {
			return fmt.Errorf("upgrade expand: rewind pipeline cursor: %w", err)
		}
	}

	// Re-enter the original orchestrator: it reads the rewound cursor and
	// dispatches the incomplete stages, which now resolve full-mode limits.
	return s.Orchestrate(ctx, userId, wec.WebEntityID.Hex())
}

// dedupeOutcome is the result of the merged old∪new variant dedupe: candidate
// keywords that survived (to insert) and existing keyword docs that lost their
// variant group (to delete).
type dedupeOutcome struct {
	insert    []models.Keyword
	deleteIDs []primitive.ObjectID
}

// dedupeMergedKeywords runs the score-based variant dedupe over the union of
// existing keyword docs and fresh candidates — old keywords compete instead of
// auto-winning. A dupe is an exact canonical-key collision (stemmed, sorted
// tokens) or a stemmed token-set Jaccard ≥ threshold against a kept keyword.
// Rules:
//   - protected existing keywords (scheduled-article row, cluster pillar)
//     always survive and seed the kept set, so any variant of them loses;
//   - everyone else competes strongest-first on a provisional opportunity
//     score computed with the funnel term at its default weight for BOTH
//     sides (variants of one phrase share a funnel, so a stored
//     classification must not bias the comparison); volume then CPC break
//     ties, and an existing doc beats a candidate on a full tie so
//     equal-value docs aren't churned;
//   - a loser that is an existing doc lands in deleteIDs (old-vs-old dupes
//     collapse too); a losing candidate is simply dropped.
func dedupeMergedKeywords(candidates, existing []models.Keyword, protected map[primitive.ObjectID]struct{}, sc config.ScoringValues, userDR int) dedupeOutcome {
	type keptEntry struct {
		key    string
		tokens map[string]struct{}
	}
	kept := make([]keptEntry, 0, len(existing)+len(candidates))
	seenExact := make(map[string]struct{}, len(existing)+len(candidates))
	keep := func(key string) {
		seenExact[key] = struct{}{}
		kept = append(kept, keptEntry{key: key, tokens: tokenSet(key)})
	}
	isDupe := func(key string) bool {
		if _, dup := seenExact[key]; dup {
			return true
		}
		tokens := tokenSet(key)
		for _, k := range kept {
			if jaccard(tokens, k.tokens) >= jaccardDupeThreshold {
				return true
			}
		}
		return false
	}

	type contender struct {
		kw       models.Keyword
		existing bool
		score    float64
	}
	pool := make([]contender, 0, len(existing)+len(candidates))
	for _, kw := range existing {
		if _, ok := protected[kw.ID]; ok {
			if key := canonicalKeywordKey(kw.Keyword); key != "" {
				keep(key)
			}
			continue
		}
		pool = append(pool, contender{kw: kw, existing: true})
	}
	for _, kw := range candidates {
		pool = append(pool, contender{kw: kw})
	}
	for i := range pool {
		pool[i].score = computeOpportunityScore(sc, pool[i].kw.Volume, pool[i].kw.KeywordDifficulty, "", userDR)
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].score != pool[j].score {
			return pool[i].score > pool[j].score
		}
		if pool[i].kw.Volume != pool[j].kw.Volume {
			return pool[i].kw.Volume > pool[j].kw.Volume
		}
		if pool[i].kw.CPC != pool[j].kw.CPC {
			return pool[i].kw.CPC > pool[j].kw.CPC
		}
		return pool[i].existing && !pool[j].existing
	})

	var out dedupeOutcome
	for _, c := range pool {
		key := canonicalKeywordKey(c.kw.Keyword)
		if key == "" {
			// Unparseable text: never delete a stored doc over it, never
			// insert it.
			continue
		}
		if isDupe(key) {
			if c.existing {
				out.deleteIDs = append(out.deleteIDs, c.kw.ID)
			}
			continue
		}
		keep(key)
		if !c.existing {
			out.insert = append(out.insert, c.kw)
		}
	}
	return out
}

// normalizeKeywordText lowers, trims, collapses whitespace, and strips light
// punctuation so cosmetic variants compare equal.
func normalizeKeywordText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		case strings.ContainsRune(".,!?'\"`:;()[]&-_/", r):
			// Light punctuation becomes a separator so "how-to" ≈ "how to".
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func tokenSet(normalized string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, tok := range strings.Fields(normalized) {
		set[tok] = struct{}{}
	}
	return set
}

// jaccard is |A∩B| / |A∪B| over token sets; empty-vs-empty is 0 (never a dupe).
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
