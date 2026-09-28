package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- images.alt_semantics ---

type imagesAltSemantics struct{}

func (imagesAltSemantics) ID() core.CheckID          { return "images.alt_semantics" }
func (imagesAltSemantics) Category() core.CategoryID { return core.CategoryImages }
func (imagesAltSemantics) Kind() CheckKind           { return KindDeterministic }
func (imagesAltSemantics) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (imagesAltSemantics) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)

	total, good := 0, 0
	var missingAltPages, junkAltPages []string
	for _, p := range pages {
		pageMissing, pageJunk := false, false
		seenSrc := map[string]bool{}
		for _, img := range p.Images {
			// The same chrome image (logo, badge) repeats across a page —
			// assess each source once.
			if img.Src != "" && seenSrc[img.Src] {
				continue
			}
			seenSrc[img.Src] = true
			total++
			alt := strings.TrimSpace(img.Alt)
			switch {
			case img.HasAlt && alt == "":
				// Explicit alt="" is the CORRECT decorative-image markup — the
				// exact practice the recommendation endorses must not fail it.
				good++
			case !img.HasAlt:
				pageMissing = true
			case len(alt) < 5 || strings.EqualFold(alt, "image") || strings.EqualFold(alt, "photo") || looksLikeFilename(alt):
				pageJunk = true
			default:
				good++
			}
		}
		if pageMissing {
			missingAltPages = append(missingAltPages, p.URL)
		}
		if pageJunk {
			junkAltPages = append(junkAltPages, p.URL)
		}
	}
	if total == 0 {
		// Zero images extracted — nothing assessed, so nothing to praise.
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	if len(missingAltPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "images.alt_semantics",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("Images without alt text on %d sampled page(s)", len(missingAltPages)),
			Detail:         "Missing alt text loses image-search visibility and fails accessibility.",
			Pages:          capPages(missingAltPages),
			Recommendation: "Write descriptive alt text for every content image (decorative images get alt=\"\").",
			Falsifiability: "Every content <img> on the listed pages carries a non-empty descriptive alt attribute.",
		})
	}
	if len(junkAltPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "images.alt_semantics",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("Placeholder alt text on %d sampled page(s)", len(junkAltPages)),
			Detail:         "Alts like \"image\", filenames, or few-character stubs describe nothing.",
			Pages:          capPages(junkAltPages),
			Recommendation: "Replace placeholder alts with what the image actually shows, in context.",
			Falsifiability: "No sampled image's alt is a filename, \"image\"/\"photo\", or under 5 characters.",
		})
	}

	return core.CheckResult{Score: ratioScore(good, total), Findings: findings}, nil
}

func looksLikeFilename(alt string) bool {
	lower := strings.ToLower(alt)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".svg"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// --- images.sizing (the 3-tier table thumbnail/content/hero — fidelity
// must, scope §5: seo-images' table wins over the reference's conflicting
// numbers) ---

type imagesSizing struct{}

func (imagesSizing) ID() core.CheckID          { return "images.sizing" }
func (imagesSizing) Category() core.CategoryID { return core.CategoryImages }
func (imagesSizing) Kind() CheckKind           { return KindDeterministic }

// Requires: HTMLDeep only — the crawl was declared historically but never
// read, and a failed crawl needlessly skipped this check.
func (imagesSizing) Requires() []core.Kind {
	return []core.Kind{artifacts.KindHTMLDeep}
}

func (imagesSizing) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)

	total, good := 0, 0
	var noDimsPages, oversizedPages []string
	for _, p := range pages {
		pageNoDims, pageOversized := false, false
		for _, img := range p.Images {
			total++
			switch {
			case (img.Width == 0 || img.Height == 0) && !img.DimensionsDeclared:
				pageNoDims = true
			case img.Oversized:
				pageOversized = true
			default:
				good++
			}
		}
		if pageNoDims {
			noDimsPages = append(noDimsPages, p.URL)
		}
		if pageOversized {
			oversizedPages = append(oversizedPages, p.URL)
		}
	}
	if total == 0 {
		// Zero images extracted — nothing assessed, so nothing to praise.
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	if len(noDimsPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "images.sizing",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("Images without declared dimensions on %d sampled page(s)", len(noDimsPages)),
			Detail:         "Missing width/height attributes force layout shifts as images load (a direct CLS cause).",
			Pages:          capPages(noDimsPages),
			Recommendation: "Declare width and height (or aspect-ratio) on every image.",
			Falsifiability: "Every sampled <img> carries width and height attributes and CLS attributable to images drops to zero.",
		})
	}
	if len(oversizedPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "images.sizing",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("Oversized images for their tier on %d sampled page(s)", len(oversizedPages)),
			Detail:         "Images declared far beyond their display tier (thumbnail ≤ 400px, content ≤ 1200px, hero ≤ 2000px wide) ship wasted bytes.",
			Pages:          capPages(oversizedPages),
			Recommendation: "Serve responsive sizes per tier (srcset) instead of one oversized original.",
			Falsifiability: "No sampled image's declared width exceeds its tier ceiling.",
		})
	}

	return core.CheckResult{Score: ratioScore(good, total), Findings: findings}, nil
}
