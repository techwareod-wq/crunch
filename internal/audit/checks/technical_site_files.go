package checks

import (
	"context"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- technical.site_files (quality uplift 3.4 — intrinsically findings-only,
// like content.parasite_markers: small conventional files worth a nudge,
// never points) ---

type technicalSiteFiles struct{}

func (technicalSiteFiles) ID() core.CheckID          { return "technical.site_files" }
func (technicalSiteFiles) Category() core.CategoryID { return core.CategoryTechnical }
func (technicalSiteFiles) Kind() CheckKind           { return KindDeterministic }
func (technicalSiteFiles) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (technicalSiteFiles) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	sf := deep.SiteFiles
	if !sf.Probed {
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	if !sf.ManifestFound {
		findings = append(findings, core.Finding{
			CheckID:        "technical.site_files",
			Severity:       core.SeverityInfo,
			Title:          "No web app manifest found",
			Detail:         "Neither /manifest.json nor /site.webmanifest answered. For mobile-first audiences a manifest enables add-to-homescreen and proper mobile theming — a twenty-minute add.",
			Recommendation: "Ship a site.webmanifest with name, icons, and theme_color, linked from <head>.",
			Falsifiability: "GET /site.webmanifest (or /manifest.json) returns 200 with valid JSON.",
		})
	}
	if !sf.SecurityTxtFound {
		findings = append(findings, core.Finding{
			CheckID:        "technical.site_files",
			Severity:       core.SeverityInfo,
			Title:          "No security.txt found",
			Detail:         "/.well-known/security.txt is table stakes for sites handling PII and appears in security-diligence checklists — it tells researchers where to report vulnerabilities instead of tweeting them.",
			Recommendation: "Publish /.well-known/security.txt with a Contact: line and an Expires: date (RFC 9116).",
			Falsifiability: "GET /.well-known/security.txt returns 200 with a Contact field.",
		})
	}

	// Intrinsically findings-only: Score nil (the spec carries no allocation).
	return core.CheckResult{Score: nil, Findings: findings}, nil
}
