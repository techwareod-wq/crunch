package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// CWV field thresholds — the canonical numbers (scope §5): LCP < 2.5s,
// INP < 200ms, CLS < 0.1. INP, not FID — FID is retired and never mentioned.
const (
	cwvLCPGoodMs = 2500.0
	cwvLCPPoorMs = 4000.0
	cwvINPGoodMs = 200.0
	cwvINPPoorMs = 500.0
	cwvCLSGood   = 0.1
	cwvCLSPoor   = 0.25
)

// Animation-heaviness thresholds for the no-field-data INP nudge: either a
// pile of inline SVGs (decorative animation rigs) or repeated animate
// class/keyframe markers on the homepage.
const (
	animationHeavySVGs  = 8
	animationHeavyHints = 5
)

// --- performance.cwv (CrUX field data — decision 6) ---

type performanceCWV struct{}

func (performanceCWV) ID() core.CheckID          { return "performance.cwv" }
func (performanceCWV) Category() core.CategoryID { return core.CategoryPerformance }
func (performanceCWV) Kind() CheckKind           { return KindDeterministic }
func (performanceCWV) Requires() []core.Kind     { return []core.Kind{artifacts.KindPSI} }

func (performanceCWV) Run(_ context.Context, in Input) (core.CheckResult, error) {
	psi, _ := in.Bundle.PSI()
	if !psi.HasFieldData {
		// No CrUX traffic → the metric is unmeasurable, not failing. Report
		// the constraint; the allocation renormalizes away (§8.3).
		findings := []core.Finding{{
			CheckID:        "performance.cwv",
			Severity:       core.SeverityInfo,
			Title:          "No Core Web Vitals field data available",
			Detail:         "The Chrome UX Report has too little real-user traffic for this origin to publish field metrics, so CWV was not scored. Our scores are heuristics — we say so rather than guessing.",
			Recommendation: "No action possible until the site accrues CrUX-eligible traffic; use lab tools directionally in the meantime.",
			Falsifiability: "PageSpeed Insights shows an originLoadingExperience block for the origin.",
		}}

		// Animation-heaviness nudge (opportunistic deep read, zero score
		// weight): exactly the site that can't see its INP yet is the one an
		// animation-heavy homepage bites once real users arrive — INP is
		// where rotating heroes and constant motion fail on mid-range mobile.
		if deep, ok := in.Bundle.HTMLDeep(); ok {
			for _, p := range deep.Pages {
				if !p.IsHomepage || !p.Fetched {
					continue
				}
				if p.InlineSVGCount >= animationHeavySVGs || p.AnimationHintCount >= animationHeavyHints {
					findings = append(findings, core.Finding{
						CheckID:        "performance.cwv",
						Severity:       core.SeverityInfo,
						Title:          "Animation-heavy homepage — watch INP once field data exists",
						Detail:         fmt.Sprintf("The homepage carries %d inline SVG(s) and %d animation marker(s). Lab runs on fast hardware hide it, but INP is where animation-heavy pages typically fail on mid-range mobile — the profile CrUX will eventually measure.", p.InlineSVGCount, p.AnimationHintCount),
						Recommendation: "Keep animations compositor-only (transform/opacity), pause offscreen loops, and re-check INP in PSI once the origin has field data.",
						Falsifiability: "This is informational; presence or absence changes no score.",
					})
				}
				break
			}
		}

		return core.CheckResult{Score: nil, Findings: findings}, nil
	}

	// Per-metric grading: good = full, needs-improvement = half, poor = 0.
	grade := func(v, good, poor float64) float64 {
		switch {
		case v <= good:
			return 1
		case v <= poor:
			return 0.5
		default:
			return 0
		}
	}
	// CrUX often publishes only some metrics (INP needs interaction volume).
	// An absent metric reads as exact zero — grade(0)=perfect — so average
	// only the metrics actually measured. (A true 0ms LCP/INP is physically
	// impossible; an exactly-0.0 CLS forgoes its credit, renormalizing over
	// the measured metrics — a conservative trade.)
	measured := 0.0
	sum := 0.0
	lcp, inp, cls := 1.0, 1.0, 1.0
	if psi.OriginLCPMs > 0 {
		lcp = grade(psi.OriginLCPMs, cwvLCPGoodMs, cwvLCPPoorMs)
		sum, measured = sum+lcp, measured+1
	}
	if psi.OriginINPMs > 0 {
		inp = grade(psi.OriginINPMs, cwvINPGoodMs, cwvINPPoorMs)
		sum, measured = sum+inp, measured+1
	}
	if psi.OriginCLS > 0 {
		cls = grade(psi.OriginCLS, cwvCLSGood, cwvCLSPoor)
		sum, measured = sum+cls, measured+1
	}
	if measured == 0 {
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	metricFinding := func(ratio float64, name, value, target, rec string) {
		if ratio >= 1 {
			return
		}
		sev := core.SeverityMedium
		if ratio == 0 {
			sev = core.SeverityHigh
		}
		findings = append(findings, core.Finding{
			CheckID:        "performance.cwv",
			Severity:       sev,
			Title:          fmt.Sprintf("%s fails the Core Web Vitals target", name),
			Detail:         fmt.Sprintf("Origin field data (75th percentile): %s — target %s. Field data is what ranking actually uses; lab timings from the crawl are directional only.", value, target),
			Recommendation: rec,
			Falsifiability: fmt.Sprintf("After ~28 days of the fix being live, PSI's origin field %s meets the target.", name),
		})
	}
	metricFinding(lcp, "LCP", fmt.Sprintf("%.1fs", psi.OriginLCPMs/1000), "< 2.5s",
		"Optimize the largest above-the-fold element: preload the hero image/font, cut render-blocking resources, use a CDN.")
	metricFinding(inp, "INP", fmt.Sprintf("%.0fms", psi.OriginINPMs), "< 200ms",
		"Break up long main-thread tasks, defer non-critical JS, keep handlers light.")
	metricFinding(cls, "CLS", fmt.Sprintf("%.2f", psi.OriginCLS), "< 0.1",
		"Reserve space for images/embeds/ads (explicit dimensions) and avoid late-inserted banners.")

	earned := sum / measured * 100
	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}

// --- performance.speculation_bfcache (decision 11) ---

type performanceSpeculationBfcache struct{}

func (performanceSpeculationBfcache) ID() core.CheckID          { return "performance.speculation_bfcache" }
func (performanceSpeculationBfcache) Category() core.CategoryID { return core.CategoryPerformance }
func (performanceSpeculationBfcache) Kind() CheckKind           { return KindDeterministic }
func (performanceSpeculationBfcache) Requires() []core.Kind {
	return []core.Kind{artifacts.KindHTMLDeep}
}

func (performanceSpeculationBfcache) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	hasSpeculation := false
	var unloadPages []string
	for _, p := range pages {
		if p.HasSpeculationRules {
			hasSpeculation = true
		}
		if p.HasUnloadHandler {
			unloadPages = append(unloadPages, p.URL)
		}
	}

	earned := 0.0
	var findings []core.Finding
	if hasSpeculation {
		earned += 40
	} else {
		findings = append(findings, core.Finding{
			CheckID:        "performance.speculation_bfcache",
			Severity:       core.SeverityInfo,
			Title:          "No Speculation Rules found",
			Detail:         "The sampled pages ship no <script type=\"speculationrules\"> — near-instant navigations via prerendering are being left on the table.",
			Recommendation: "Add conservative prerender speculation rules for likely next pages.",
			Falsifiability: "Sampled pages include a parsed speculationrules script and chrome://predictors shows prerender activity.",
		})
	}
	if len(unloadPages) == 0 {
		earned += 60
	} else {
		findings = append(findings, core.Finding{
			CheckID:        "performance.speculation_bfcache",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) block the back/forward cache", len(unloadPages)),
			Pages:          capPages(unloadPages),
			Detail:         "unload/beforeunload handlers disqualify pages from bfcache, making every back navigation a full reload.",
			Recommendation: "Replace unload/beforeunload with pagehide/visibilitychange.",
			Falsifiability: "DevTools' back/forward cache test passes on each listed page.",
		})
	}

	// Cache-Control: no-store on the homepage document is the other bfcache
	// killer — the header probe already captured it. Findings-only (zero
	// score weight — the 40/60 split above is the spec'd allocation).
	if deep, ok := in.Bundle.HTMLDeep(); ok && deep.Headers.Fetched && deep.Headers.CacheControlNoStore {
		findings = append(findings, core.Finding{
			CheckID:        "performance.speculation_bfcache",
			Severity:       core.SeverityInfo,
			Title:          "Homepage served with Cache-Control: no-store",
			Detail:         "no-store on the document disqualifies it from the back/forward cache — every back navigation becomes a full reload, regardless of unload handlers.",
			Recommendation: "Scope no-store to the responses that genuinely need it (authenticated/API responses); serve the document with a cacheable policy.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}

	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}

// --- performance.psi_opportunities (top opportunities as findings + minor
// score) ---

type performancePSIOpportunities struct{}

func (performancePSIOpportunities) ID() core.CheckID          { return "performance.psi_opportunities" }
func (performancePSIOpportunities) Category() core.CategoryID { return core.CategoryPerformance }
func (performancePSIOpportunities) Kind() CheckKind           { return KindDeterministic }
func (performancePSIOpportunities) Requires() []core.Kind     { return []core.Kind{artifacts.KindPSI} }

func (performancePSIOpportunities) Run(_ context.Context, in Input) (core.CheckResult, error) {
	psi, _ := in.Bundle.PSI()

	var findings []core.Finding
	bigSavings := 0
	for i, op := range psi.Opportunities {
		if i >= 5 {
			break
		}
		if op.SavingsMs < 250 {
			continue
		}
		if op.SavingsMs >= 1000 {
			bigSavings++
		}
		sev := core.SeverityLow
		if op.SavingsMs >= 1000 {
			sev = core.SeverityMedium
		}
		findings = append(findings, core.Finding{
			CheckID:        "performance.psi_opportunities",
			Severity:       sev,
			Title:          op.Title,
			Detail:         fmt.Sprintf("Lighthouse estimates ~%.1fs of load-time savings from this change (lab estimate — directional, not a field metric).", op.SavingsMs/1000),
			Recommendation: op.Title + ".",
			Falsifiability: fmt.Sprintf("A fresh Lighthouse run no longer lists %q as an opportunity.", op.ID),
		})
	}

	// Score off the lab performance score, tempered — this slice is 15/100 of
	// a 9-weight category by design (field data dominates). A zero lab score
	// with no lab category parsed is "not assessed" (previously it scored
	// 0/100 when audits parsed but the category didn't, and a free 100 when
	// nothing parsed at all).
	evidence := map[string]any{}
	if psi.LabPerformanceScore <= 0 {
		return core.CheckResult{Score: nil, Findings: findings, Evidence: evidence}, nil
	}
	evidence["labPerformanceScore"] = psi.LabPerformanceScore
	return core.CheckResult{Score: fixedScore(psi.LabPerformanceScore), Findings: findings, Evidence: evidence}, nil
}
