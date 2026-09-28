package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- onpage.head_hygiene (quality uplift 2.4 — social meta + lang; per
// sampled page: og:title+og:image 40 · twitter:card 15 · twitter:site 5 ·
// <html lang> 40) ---

// usLocationCode is DataForSEO's United States market — the resolver's
// default, which the market-vs-lang nudge must never treat as a known
// non-US market.
const usLocationCode = 2840

type onpageHeadHygiene struct{}

func (onpageHeadHygiene) ID() core.CheckID          { return "onpage.head_hygiene" }
func (onpageHeadHygiene) Category() core.CategoryID { return core.CategoryOnPage }
func (onpageHeadHygiene) Kind() CheckKind           { return KindDeterministic }
func (onpageHeadHygiene) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (onpageHeadHygiene) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		return core.CheckResult{Score: fixedScore(100)}, nil
	}

	total := 0.0
	var noOG, noLang []string
	cardWithoutSite := false
	langMismatch := false
	var langMismatchDetail string
	homepageBareEn := false
	for _, p := range pages {
		earned := 0.0
		if p.HasOGTitle && p.HasOGImage {
			earned += 40
		} else {
			noOG = append(noOG, p.URL)
		}
		if p.HasTwitterCard {
			earned += 15
			if p.HasTwitterSite {
				earned += 5
			} else {
				cardWithoutSite = true
			}
		}
		if p.PageLang != "" {
			earned += 40
		} else {
			noLang = append(noLang, p.URL)
		}
		// Only a real region subtag ("en-US") counts — values like "English"
		// declare no region and must not fire the regional-mismatch nudge.
		if strings.EqualFold(p.PageLang, "en") && p.SchemaInLanguage != "" &&
			strings.HasPrefix(strings.ToLower(p.SchemaInLanguage), "en-") {
			langMismatch = true
			langMismatchDetail = fmt.Sprintf("<html lang=\"en\"> vs schema inLanguage %q", p.SchemaInLanguage)
		}
		if p.IsHomepage && strings.EqualFold(p.PageLang, "en") {
			homepageBareEn = true
		}
		total += earned
	}

	var findings []core.Finding
	if len(noOG) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.head_hygiene",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d page(s) missing Open Graph tags", len(noOG)),
			Detail:         "Without og:title/og:image, every share into Slack, WhatsApp, LinkedIn, or iMessage renders as a bare link — shares attribute to nobody.",
			Pages:          capPages(noOG),
			Recommendation: "Add og:title, og:description, and a 1200×630 og:image to every page (plus twitter:card summary_large_image).",
			Falsifiability: "Pasting each listed URL into a social/messaging app unfurls a rich preview card.",
		})
	}
	if len(noLang) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.head_hygiene",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d page(s) without a lang attribute on <html>", len(noLang)),
			Detail:         "The lang attribute is how search engines, screen readers, and translation layers know the page's language — omitting it forces guessing.",
			Pages:          capPages(noLang),
			Recommendation: "Set <html lang=\"…\"> to the page's actual language (with region where it matters, e.g. en-IN).",
			Falsifiability: "View source on each listed page: the <html> element carries a lang attribute.",
		})
	}
	if cardWithoutSite {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.head_hygiene",
			Severity:       core.SeverityLow,
			Title:          "twitter:card present but no twitter:site handle",
			Detail:         "The card renders, but without twitter:site the share attributes to nobody — the brand handle is the piece that turns a share into a brand impression.",
			Recommendation: "Add the site's X/Twitter handle as a twitter:site meta tag on every page.",
			Snippet:        `<meta name="twitter:site" content="@yourhandle">`,
			Falsifiability: "View source on any page: a twitter:site meta tag carries the brand's @handle.",
		})
	}
	if langMismatch {
		findings = append(findings, core.Finding{
			CheckID:        "onpage.head_hygiene",
			Severity:       core.SeverityInfo,
			Title:          "lang attribute is bare \"en\" while schema declares a regional language",
			Detail:         fmt.Sprintf("Inconsistent language signals (%s). We can't know the intended target market from the outside — this is a consistency nudge, not a correctness claim.", langMismatchDetail),
			Recommendation: "Align <html lang> with the schema's inLanguage (use the regional form if the audience is regional).",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}

	// Market-vs-lang nudge (zero score weight): the run resolved a non-US
	// ranking market, yet the homepage declares bare "en" — the one free
	// region signal is pointing away from the actual audience. Only fires
	// when the market is KNOWN (tenant setting or ccTLD; a lang-derived
	// market can't be bare by construction).
	if homepageBareEn && !langMismatch && in.Run != nil &&
		in.Run.LocationCode != 0 && in.Run.LocationCode != usLocationCode {
		market := in.Run.LocationName
		if market == "" {
			market = "the resolved target market"
		}
		findings = append(findings, core.Finding{
			CheckID:        "onpage.head_hygiene",
			Severity:       core.SeverityInfo,
			Title:          fmt.Sprintf("Homepage declares bare lang=\"en\" on a site targeting %s", market),
			Detail:         fmt.Sprintf("The audit resolved this site's ranking market as %s, but <html lang> carries no region. A regional subtag (e.g. en-IN for India) plus a matching schema inLanguage is a small, free signal — and right now it's absent.", market),
			Recommendation: "Set <html lang> to the regional form for the target market and mirror it as inLanguage in the WebSite schema. One locale needs no hreflang.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}

	return core.CheckResult{Score: fixedScore(total / float64(len(pages))), Findings: findings}, nil
}
