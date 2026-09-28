package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- technical.soft_404 (quality uplift 3.3 — one probe of a URL that
// cannot exist; a site answering it 200 poisons crawl budget and index
// quality for every real broken URL) ---

type technicalSoft404 struct{}

func (technicalSoft404) ID() core.CheckID          { return "technical.soft_404" }
func (technicalSoft404) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalSoft404) Kind() CheckKind           { return KindDeterministic }
func (technicalSoft404) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (technicalSoft404) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	probe := deep.Soft404
	if !probe.Fetched {
		// Probe never landed (network failure or a pre-uplift artifact) —
		// not assessed: no score, the category renormalizes. Never guess.
		return core.CheckResult{Score: nil}, nil
	}

	switch {
	case probe.Status == 404 || probe.Status == 410:
		return core.CheckResult{Score: fixedScore(100)}, nil

	case probe.Status == 200:
		return core.CheckResult{
			Score: fixedScore(0),
			Findings: []core.Finding{{
				CheckID:        "technical.soft_404",
				Severity:       core.SeverityHigh,
				Title:          "Nonexistent URLs return 200 (soft-404)",
				Detail:         fmt.Sprintf("A GET of a guaranteed-nonexistent path answered HTTP %d. Soft-404s make every broken URL look like a real page — crawl budget drains into garbage, and search engines lose trust in the site's status codes.", probe.Status),
				Recommendation: "Return a real 404 (or 410) status for unknown paths; keep the friendly error page, fix the status code.",
				Falsifiability: "GET https://<domain>/<any-random-string> returns HTTP 404/410.",
			}},
		}, nil

	case probe.Status >= 300 && probe.Status < 400 && probe.FinalStatus == 200:
		return core.CheckResult{
			Score: fixedScore(40),
			Findings: []core.Finding{{
				CheckID:        "technical.soft_404",
				Severity:       core.SeverityLow,
				Title:          "Broken URLs redirect instead of 404ing",
				Detail:         fmt.Sprintf("A nonexistent path answered %d and redirected to a 200 page. Redirecting unknown URLs (usually to the homepage) is a soft-404 pattern Google treats as such.", probe.Status),
				Recommendation: "Serve unknown paths a 404 status directly instead of redirecting them to a live page.",
				Falsifiability: "GET https://<domain>/<any-random-string> returns HTTP 404/410 without redirecting.",
			}},
		}, nil
	}

	// Other statuses (403/5xx/redirect-to-404) don't fake the page's
	// existence — not the soft-404 failure class this check measures.
	return core.CheckResult{Score: fixedScore(100)}, nil
}
