package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- backlinks.domain_rating (via the utils.FetchDomainRating task shape;
// rank_scale one_hundred) ---

type backlinksDomainRating struct{}

func (backlinksDomainRating) ID() core.CheckID          { return "backlinks.domain_rating" }
func (backlinksDomainRating) Category() core.CategoryID { return core.CategoryBacklinks }
func (backlinksDomainRating) Kind() CheckKind           { return KindDeterministic }
func (backlinksDomainRating) Requires() []core.Kind     { return []core.Kind{artifacts.KindAuthority} }

func (backlinksDomainRating) Run(_ context.Context, in Input) (core.CheckResult, error) {
	auth, _ := in.Bundle.Authority()
	if !auth.HasBacklinkData {
		return core.CheckResult{
			Score: nil,
			Findings: []core.Finding{{
				CheckID:        "backlinks.domain_rating",
				Severity:       core.SeverityInfo,
				Title:          "No backlink authority data available",
				Detail:         "The backlink index has no data for this domain (common for brand-new sites), so domain rating was not scored.",
				Recommendation: "Earn first citations: directories, partner mentions, launch coverage.",
				Falsifiability: "A later audit reports a non-zero domain rating.",
			}},
		}, nil
	}

	dr := auth.DomainRating
	var findings []core.Finding
	// The finding covers the whole sub-40 band so the slice score is always
	// explained — a 25/100 with zero findings read as an unexplained number.
	if dr < 40 {
		sev := core.SeverityMedium
		if dr >= 20 {
			sev = core.SeverityLow
		}
		findings = append(findings, core.Finding{
			CheckID:        "backlinks.domain_rating",
			Severity:       sev,
			Title:          fmt.Sprintf("Low domain rating (%d/100)", dr),
			Detail:         "Weak backlink authority caps how competitive the site's content can rank regardless of on-page quality. The rating is log-scaled: single digits are normal for young sites, and each step up gets harder.",
			Recommendation: "Run a deliberate link program: original data/research, digital PR, and unlinked-mention reclamation.",
			Falsifiability: "The domain rating measurably rises in a follow-up audit (heuristic scale — direction matters, not the decimal).",
		})
	}

	// The 0-100 rating IS the score for this slice.
	return core.CheckResult{Score: fixedScore(float64(dr)), Findings: findings}, nil
}

// --- backlinks.profile_summary (referring domains, dofollow ratio —
// summary-level only, decision 7) ---

type backlinksProfileSummary struct{}

func (backlinksProfileSummary) ID() core.CheckID          { return "backlinks.profile_summary" }
func (backlinksProfileSummary) Category() core.CategoryID { return core.CategoryBacklinks }
func (backlinksProfileSummary) Kind() CheckKind           { return KindDeterministic }
func (backlinksProfileSummary) Requires() []core.Kind     { return []core.Kind{artifacts.KindAuthority} }

func (backlinksProfileSummary) Run(_ context.Context, in Input) (core.CheckResult, error) {
	auth, _ := in.Bundle.Authority()
	if !auth.HasBacklinkData {
		return core.CheckResult{Score: nil}, nil // constraint already emitted by domain_rating
	}

	var findings []core.Finding
	earned := 0.0

	// Referring-domain breadth (log-ish banding, 50 pts).
	rd := auth.ReferringDomains
	switch {
	case rd >= 500:
		earned += 50
	case rd >= 100:
		earned += 40
	case rd >= 25:
		earned += 30
	case rd >= 5:
		earned += 15
	}
	if rd < 25 {
		findings = append(findings, core.Finding{
			CheckID:        "backlinks.profile_summary",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("Thin referring-domain base (%d domains)", rd),
			Detail:         "Authority compounds from breadth of unique linking domains more than raw link count.",
			Recommendation: "Prioritize first links from new domains over repeat links from existing ones.",
			Falsifiability: "The referring-domain count grows in the next audit.",
		})
	}

	// Dofollow share (30 pts) — only earnable when referring domains exist;
	// a zero-link domain must not collect quality points for links it
	// doesn't have.
	dofollowShare := 0.0
	if rd > 0 {
		dofollowShare = float64(rd-auth.NofollowRefDomains) / float64(rd)
		if dofollowShare < 0 {
			dofollowShare = 0
		}
		earned += dofollowShare * 30
	}
	if rd >= 10 && dofollowShare < 0.5 {
		findings = append(findings, core.Finding{
			CheckID:        "backlinks.profile_summary",
			Severity:       core.SeverityLow,
			Title:          "Backlink profile is mostly nofollow",
			Detail:         fmt.Sprintf("Only %.0f%% of referring domains link dofollow — equity flow is limited.", dofollowShare*100),
			Recommendation: "Pursue editorial (dofollow) placements: guest research, PR, partnerships.",
			Falsifiability: "The dofollow share of referring domains exceeds 50% in a later audit.",
		})
	}

	// Broken-backlink hygiene (20 pts) — only earnable when backlinks exist.
	brokenShare := 0.0
	if auth.Backlinks > 0 {
		brokenShare = float64(auth.BrokenBacklinks) / float64(auth.Backlinks)
		earned += (1 - brokenShare) * 20
	}
	if auth.BrokenBacklinks > 0 && brokenShare > 0.05 {
		findings = append(findings, core.Finding{
			CheckID:        "backlinks.profile_summary",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d broken backlink(s) waste earned authority", auth.BrokenBacklinks),
			Detail:         "External links pointing at dead pages on this site pass nothing.",
			Recommendation: "301 the dead targets to their closest live equivalents.",
			Falsifiability: "The broken-backlink count drops below 5% of total backlinks.",
		})
	}

	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}
