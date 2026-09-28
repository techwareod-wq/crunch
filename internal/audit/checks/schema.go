package checks

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- schema.validity (JSON-LD extraction + validation) ---

type schemaValidity struct{}

func (schemaValidity) ID() core.CheckID          { return "schema.validity" }
func (schemaValidity) Category() core.CategoryID { return core.CategorySchema }
func (schemaValidity) Kind() CheckKind           { return KindDeterministic }
func (schemaValidity) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (schemaValidity) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	withSchema, valid := 0, 0
	var noSchemaPages, brokenPages []string
	blocksTotal, blocksValid := 0, 0
	for _, p := range pages {
		if len(p.SchemaBlocks) == 0 {
			noSchemaPages = append(noSchemaPages, p.URL)
			continue
		}
		withSchema++
		pageOK := true
		for _, b := range p.SchemaBlocks {
			blocksTotal++
			if b.Valid && !b.MissingContext {
				blocksValid++
			} else {
				pageOK = false
			}
		}
		if pageOK {
			valid++
		} else {
			brokenPages = append(brokenPages, p.URL)
		}
	}

	// Placeholder text in shipped markup (findings-only, zero score impact):
	// "[Business Name]" / REPLACE_ME slots mean the template was never filled.
	var placeholderPages []string
	for _, p := range pages {
		for _, b := range p.SchemaBlocks {
			if b.HasPlaceholder {
				placeholderPages = append(placeholderPages, p.URL)
				break
			}
		}
	}

	var findings []core.Finding
	if len(placeholderPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "schema.validity",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d page(s) ship JSON-LD with template placeholder text", len(placeholderPages)),
			Detail:         "Structured data still contains placeholder slots like \"[Business Name]\" or REPLACE_ME — the markup parses, but it publishes template scaffolding as fact.",
			Pages:          capPages(placeholderPages),
			Recommendation: "Fill every placeholder with the real value (or remove the block until it's ready).",
			Falsifiability: "View source on each listed page: no bracketed placeholder or REPLACE token appears inside any JSON-LD block.",
		})
	}
	if len(noSchemaPages) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "schema.validity",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("%d sampled page(s) carry no structured data", len(noSchemaPages)),
			Detail:         "No JSON-LD found — the page competes for rich results and AI answers without machine-readable context.",
			Pages:          capPages(noSchemaPages),
			Recommendation: "Add JSON-LD appropriate to the page type (Article, Organization, Product, ...).",
			Falsifiability: "Google's Rich Results Test parses at least one valid JSON-LD block per listed page.",
		})
	}
	if len(brokenPages) > 0 {
		detail := "Structured data that fails to parse (or lacks @context) is ignored wholesale — worse than none, because it hides working markup on the same page."
		// Template-scale context (gap-closure round 4): a broken sampled page
		// from a large URL cluster is one instance of a template defect — the
		// same markup ships on every sibling, so the real blast radius is the
		// whole collection, not the sampled page.
		if note := templateScaleNote(in, brokenPages); note != "" {
			detail += " " + note
		}
		findings = append(findings, core.Finding{
			CheckID:        "schema.validity",
			Severity:       core.SeverityHigh,
			Title:          fmt.Sprintf("%d sampled page(s) with invalid JSON-LD", len(brokenPages)),
			Detail:         detail,
			Pages:          capPages(brokenPages),
			Recommendation: "Fix the JSON syntax / add @context so every block parses — fix it in the template, not per page.",
			Falsifiability: "The Rich Results Test reports zero parse errors on each listed page and on 2+ sibling pages from the same section.",
		})
	}

	// 50 points for schema coverage, 50 for block validity.
	earned := share(withSchema, len(pages))*50 + share(blocksValid, blocksTotal)*50
	if blocksTotal == 0 {
		earned = share(withSchema, len(pages)) * 50
	}
	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}

// --- schema.deprecations (2024–26 list; FAQPage is ALWAYS Info, never
// "remove" — fidelity must, scope §5) ---

// deprecatedRichResults maps schema types whose rich results Google retired
// (2024–2026) to the year of retirement. Keys MUST be real @type tokens —
// program names that never appear as @type ("CourseInfo", "EstimatedSalary",
// "LearningVideo", "VehicleListing", "BookAction") could never match, and
// keying on the live types they ride on (Course, Occupation, VideoObject,
// Car, Book) would flag markup that still earns results.
var deprecatedRichResults = map[string]string{
	"HowTo":               "2023-09 (rich results removed)",
	"HowToStep":           "2023-09 (rich results removed)",
	"FAQPage":             "2026-05 (rich results fully retired for all sites; gov/health-only since 2023-08)",
	"SpecialAnnouncement": "2025-07 (COVID-era type no longer processed)",
	"ClaimReview":         "2025-06 (fact-check rich results retired)",
}

type schemaDeprecations struct{}

func (schemaDeprecations) ID() core.CheckID          { return "schema.deprecations" }
func (schemaDeprecations) Category() core.CategoryID { return core.CategorySchema }
func (schemaDeprecations) Kind() CheckKind           { return KindDeterministic }
func (schemaDeprecations) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (schemaDeprecations) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	var findings []core.Finding
	deprecatedPages := map[string][]string{} // type → pages
	assessed, clean := 0, 0
	for _, p := range pages {
		assessed++
		pageClean := true
		for _, b := range p.SchemaBlocks {
			for _, t := range b.Types {
				if _, dep := deprecatedRichResults[t]; dep {
					deprecatedPages[t] = append(deprecatedPages[t], p.URL)
					if t != "FAQPage" && t != "HowTo" && t != "HowToStep" {
						pageClean = false
					}
				}
			}
		}
		if pageClean {
			clean++
		}
	}

	for typ, urls := range deprecatedPages {
		note := deprecatedRichResults[typ]
		if typ == "FAQPage" {
			// FAQPage findings MUST be Info: never recommend removal, never
			// claim an AI-citation benefit (locked fidelity rule).
			findings = append(findings, core.Finding{
				CheckID:        "schema.deprecations",
				Severity:       core.SeverityInfo,
				Title:          "FAQPage markup present (rich result retired)",
				Detail:         fmt.Sprintf("Google retired FAQ rich results in %s. The markup itself remains valid structured data and costs nothing — keep it if the FAQs are real; for genuine single-question community pages, QAPage is the type that still earns a result.", note),
				Pages:          capPages(urls),
				Recommendation: "No action required. Keep FAQPage markup where the page genuinely answers those questions; just don't expect a rich result from it.",
				Falsifiability: "This is informational — there is nothing to fix, so nothing can fail.",
			})
			continue
		}
		sev := core.SeverityLow
		findings = append(findings, core.Finding{
			CheckID:        "schema.deprecations",
			Severity:       sev,
			Title:          fmt.Sprintf("%s markup targets a retired rich result", typ),
			Detail:         fmt.Sprintf("%s: %s. The markup no longer earns its rich result.", typ, note),
			Pages:          capPages(urls),
			Recommendation: fmt.Sprintf("Stop investing in %s markup for rich-result purposes; migrate the content's markup to a supported type where one fits.", typ),
			Falsifiability: "Sampled pages either drop the retired type or the team consciously accepts it as plain structured data.",
		})
	}

	return core.CheckResult{Score: ratioScore(clean, assessed), Findings: findings}, nil
}
