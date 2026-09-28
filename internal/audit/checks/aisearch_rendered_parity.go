package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- aisearch.rendered_parity (quality uplift 2.2 — the #1 GEO failure
// class: the deep pass reads JS-rendered HTML, but most AI crawlers execute
// no JavaScript; content that only exists post-render is invisible to them) ---

type aisearchRenderedParity struct{}

func (aisearchRenderedParity) ID() core.CheckID          { return "aisearch.rendered_parity" }
func (aisearchRenderedParity) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchRenderedParity) Kind() CheckKind           { return KindDeterministic }
func (aisearchRenderedParity) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchRenderedParity) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)

	assessed, passing := 0, 0
	var invisible, degraded []string
	for _, p := range pages {
		// RawFetchOK false = the no-JS probe failed OR the artifact predates
		// it — either way, never guess. Zero-word rendered pages carry no
		// parity signal. RawFallback = the "rendered" document itself came
		// from the no-JS fallback, so a comparison would be raw-vs-raw and
		// trivially pass — not assessed.
		if !p.RawFetchOK || p.WordCount == 0 || p.RawFallback {
			continue
		}
		assessed++
		ratio := float64(p.RawWordCount) / float64(p.WordCount)
		switch {
		case ratio >= 0.6:
			passing++
		case ratio < 0.2:
			invisible = append(invisible, p.URL)
		default:
			degraded = append(degraded, p.URL)
		}
	}
	if assessed == 0 {
		// No page could be parity-probed — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	if len(invisible) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.rendered_parity",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%d page(s) are invisible without JavaScript", len(invisible)),
			Detail:         "Fetching these pages without JS yields under 20% of the rendered text. Most AI crawlers (and many search crawlers on their first pass) execute no JavaScript — to them these pages are effectively empty.",
			Pages:          capPages(invisible),
			Recommendation: "Server-render or statically generate the content pages so the initial HTML carries the full text (SSR/SSG/ISR — most modern frameworks support it as a config change).",
			Falsifiability: "curl of each listed URL returns HTML containing the article's full text and H1.",
		})
	}
	if len(degraded) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.rendered_parity",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) lose most of their content without JavaScript", len(degraded)),
			Detail:         "A no-JS fetch of these pages yields 20–60% of the rendered text — the core content partially depends on client-side rendering.",
			Pages:          capPages(degraded),
			Recommendation: "Move the main article body into the server-rendered HTML; hydrate interactivity on top instead of rendering content client-side.",
			Falsifiability: "A no-JS fetch of each listed URL yields at least 60% of the rendered word count.",
		})
	}

	return core.CheckResult{Score: ratioScore(passing, assessed), Findings: findings}, nil
}
