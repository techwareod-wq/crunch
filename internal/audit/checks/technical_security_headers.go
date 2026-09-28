package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- technical.security_headers (quality uplift 3.2 — scored, decision 3) ---

type technicalSecurityHeaders struct{}

func (technicalSecurityHeaders) ID() core.CheckID          { return "technical.security_headers" }
func (technicalSecurityHeaders) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalSecurityHeaders) Kind() CheckKind           { return KindDeterministic }
func (technicalSecurityHeaders) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

// securityHeaderWeights: HSTS 30 · CSP 25 · X-Content-Type-Options 15 ·
// frame protection 15 · Referrer-Policy 15 (sums 100).
func (technicalSecurityHeaders) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	h := deep.Headers
	if !h.Fetched {
		// The probe never landed (or the artifact predates it) — not
		// assessed: no score, the category renormalizes. fixedScore(100) here
		// would award full marks (and a "security headers are in place"
		// strength) on zero evidence.
		return core.CheckResult{Score: nil}, nil
	}

	earned := 0.0
	var findings []core.Finding
	miss := func(present bool, points float64, sev core.Severity, header, why, rec string) {
		if present {
			earned += points
			return
		}
		findings = append(findings, core.Finding{
			CheckID:        "technical.security_headers",
			Severity:       sev,
			Title:          fmt.Sprintf("Missing %s header", header),
			Detail:         why,
			Recommendation: rec,
			Falsifiability: fmt.Sprintf("The homepage response carries a %s header.", header),
		})
	}

	hstsSeverity := core.SeverityLow
	if crawl, ok := in.Bundle.Crawl(); ok && crawl.ValidCertificate {
		// An HTTPS site without HSTS leaves the first request downgradeable.
		hstsSeverity = core.SeverityMedium
	}
	miss(h.HSTS, 30, hstsSeverity, "Strict-Transport-Security",
		"Without HSTS, a user's first request can be intercepted and downgraded to HTTP.",
		"Add Strict-Transport-Security: max-age=31536000; includeSubDomains.")
	miss(h.CSP, 25, core.SeverityLow, "Content-Security-Policy",
		"No CSP means any injected script runs with full page privileges.",
		"Ship a Content-Security-Policy — start with a report-only policy and tighten.")
	miss(h.XContentTypeOptions, 15, core.SeverityLow, "X-Content-Type-Options",
		"Without nosniff, browsers may MIME-sniff responses into executable types.",
		"Add X-Content-Type-Options: nosniff.")
	miss(h.FrameProtection, 15, core.SeverityLow, "X-Frame-Options / frame-ancestors",
		"Without frame protection the site can be embedded and clickjacked.",
		"Add X-Frame-Options: DENY or a CSP frame-ancestors directive.")
	miss(h.ReferrerPolicy, 15, core.SeverityLow, "Referrer-Policy",
		"Full referrer URLs (including paths and query strings) leak to every external destination.",
		"Add Referrer-Policy: strict-origin-when-cross-origin.")

	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}
