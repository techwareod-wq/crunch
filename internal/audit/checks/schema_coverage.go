package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- schema.coverage (quality uplift 2.3 — validity checks what EXISTS;
// this checks what's MISSING: an exemplary blog with a naked homepage is an
// inverted priority) ---

type schemaCoverage struct{}

func (schemaCoverage) ID() core.CheckID          { return "schema.coverage" }
func (schemaCoverage) Category() core.CategoryID { return core.CategorySchema }
func (schemaCoverage) Kind() CheckKind           { return KindDeterministic }
func (schemaCoverage) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

// articleTypes are the schema types that mark an article page as covered —
// including the Article subtypes real CMSes emit.
var articleTypes = map[string]bool{
	"Article": true, "BlogPosting": true, "NewsArticle": true,
	"TechArticle": true, "Report": true, "ScholarlyArticle": true,
	"LiveBlogPosting": true, "SocialMediaPosting": true,
}

// orgEntityType matches Organization and its common subtypes (LocalBusiness,
// Corporation, ...) — an exact match on the literal string fired "missing
// entity-level schema" against correctly marked-up sites.
func orgEntityType(t string) bool {
	if t == "Organization" || strings.HasSuffix(t, "Organization") ||
		t == "LocalBusiness" || strings.HasSuffix(t, "Business") || t == "Corporation" {
		return true
	}
	switch t {
	case "Restaurant", "Store", "Hotel", "Dentist", "Attorney", "Physician",
		"ProfessionalService", "FinancialService", "LegalService",
		"RealEstateAgent", "InsuranceAgency", "TravelAgency", "AutoRepair",
		"BeautySalon", "MedicalClinic", "Airline", "Brand":
		return true
	}
	return false
}

func (schemaCoverage) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	hasType := func(p artifacts.DeepPage, want func(string) bool) bool {
		for _, b := range p.SchemaBlocks {
			for _, t := range b.Types {
				if want(t) {
					return true
				}
			}
		}
		return false
	}

	earned, possible := 0.0, 0.0
	var findings []core.Finding

	// Homepage layer: Organization 30 · WebSite 30.
	for _, p := range pages {
		if !p.IsHomepage {
			continue
		}
		possible += 60
		orgPresent := hasType(p, orgEntityType)
		if orgPresent {
			earned += 30
		}
		if hasType(p, func(t string) bool { return t == "WebSite" }) {
			earned += 30
		}
		if !orgPresent || !hasType(p, func(t string) bool { return t == "WebSite" }) {
			findings = append(findings, core.Finding{
				CheckID:        "schema.coverage",
				Severity:       core.SeverityMedium,
				Title:          "Homepage is missing entity-level schema",
				Detail:         "The homepage lacks Organization and/or WebSite markup — the blocks that tell search and AI engines WHO the site is, independent of any article.",
				Pages:          []string{p.URL},
				Recommendation: "Add Organization (name, url, logo, sameAs) and WebSite JSON-LD to the homepage.",
				Falsifiability: "The Rich Results Test parses Organization and WebSite blocks on the homepage.",
			})
		}
		if orgPresent && p.HasOrganizationSchema && (!p.OrganizationHasLogo || !p.OrganizationHasURL) {
			findings = append(findings, core.Finding{
				CheckID:        "schema.coverage",
				Severity:       core.SeverityInfo,
				Title:          "Organization schema lacks logo/url",
				Detail:         "The Organization block exists but omits logo and/or url — the two fields knowledge panels and entity resolution lean on.",
				Pages:          []string{p.URL},
				Recommendation: "Add logo and url to the Organization block.",
				Falsifiability: "This is informational; presence or absence changes no score.",
			})
		}
		break
	}

	// Article layer: byline-or-dated sampled pages should carry an Article
	// type — prorated 40.
	articleLike, covered := 0, 0
	var uncovered []string
	for _, p := range pages {
		if p.IsHomepage || (!p.HasByline && !p.HasDates) {
			continue
		}
		articleLike++
		if hasType(p, func(t string) bool { return articleTypes[t] }) {
			covered++
		} else {
			uncovered = append(uncovered, p.URL)
		}
	}
	if articleLike > 0 {
		possible += 40
		earned += 40 * float64(covered) / float64(articleLike)
		if len(uncovered) > 0 {
			findings = append(findings, core.Finding{
				CheckID:        "schema.coverage",
				Severity:       core.SeverityLow,
				Title:          fmt.Sprintf("%d article page(s) without Article schema", len(uncovered)),
				Detail:         "Pages with bylines/dates but no Article/BlogPosting markup forfeit article rich results and machine-readable authorship.",
				Pages:          capPages(uncovered),
				Recommendation: "Add Article or BlogPosting JSON-LD (headline, author, datePublished, dateModified) to each article.",
				Falsifiability: "The Rich Results Test parses an Article-type block on each listed page.",
			})
		}
	}

	// FAQ-shaped content heuristic — findings-only, too fuzzy to punish.
	var faqShaped []string
	for _, p := range pages {
		if p.QuestionHeadings >= 3 && !hasType(p, func(t string) bool { return t == "FAQPage" }) {
			faqShaped = append(faqShaped, p.URL)
		}
	}
	if len(faqShaped) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "schema.coverage",
			Severity:       core.SeverityInfo,
			Title:          fmt.Sprintf("%d page(s) with FAQ-shaped content but no FAQPage markup", len(faqShaped)),
			Detail:         "Pages built around 3+ question headings carry no FAQPage schema. The rich result is limited these days, but the markup remains valid machine-readable structure and costs nothing.",
			Pages:          capPages(faqShaped),
			Recommendation: "Optional: mirror the on-page Q&A in FAQPage JSON-LD where the questions are real.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}

	if possible == 0 {
		// Neither a homepage nor an article-like page in the sample — nothing
		// this check measures was assessed.
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	return core.CheckResult{Score: &core.Score{Earned: earned, Possible: possible}, Findings: findings}, nil
}
