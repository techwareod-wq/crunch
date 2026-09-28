package engine

import (
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
)

// recheckSeverityWeight orders severities for the burden comparison. Info
// findings are transparency notes, never a burden.
var recheckSeverityWeight = map[core.Severity]int{
	core.SeverityCritical: 8,
	core.SeverityHigh:     4,
	core.SeverityMedium:   2,
	core.SeverityLow:      1,
	core.SeverityInfo:     0,
}

// recheckBurden collapses one side into a comparable weight: severity-weighted
// finding sum, with the affected-page count as a secondary signal (a finding
// that shrank from 7 pages to 3 improved even at the same severity).
func recheckBurden(side *models.AuditRecheckSide) (weight int, pages int) {
	if side == nil {
		return 0, 0
	}
	for _, f := range side.Findings {
		weight += recheckSeverityWeight[f.Severity]
	}
	return weight, side.Pages
}

// RecheckVerdict compares the original outcome against the re-collected one —
// pure code, no LLM: severity-weighted findings first, affected pages second,
// the earned ratio as the tiebreak for score-feeding checks.
func RecheckVerdict(before, after *models.AuditRecheckSide) string {
	bw, bp := recheckBurden(before)
	aw, ap := recheckBurden(after)

	switch {
	case aw == 0 && ap == 0:
		// Re-ran clean. "Fixed" even when before was clean too — clean is
		// the claim the chip makes.
		return models.AuditRecheckVerdictFixed
	case aw < bw, aw == bw && ap < bp:
		return models.AuditRecheckVerdictImproved
	case aw > bw, aw == bw && ap > bp:
		return models.AuditRecheckVerdictRegressed
	}

	// Same burden — let the score ratio break the tie for scored checks.
	if before != nil && after != nil && before.HasScore && after.HasScore &&
		before.Possible > 0 && after.Possible > 0 {
		br := before.Earned / before.Possible
		ar := after.Earned / after.Possible
		const epsilon = 0.01
		if ar > br+epsilon {
			return models.AuditRecheckVerdictImproved
		}
		if ar < br-epsilon {
			return models.AuditRecheckVerdictRegressed
		}
	}
	return models.AuditRecheckVerdictUnchanged
}
