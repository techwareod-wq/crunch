package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// --- aisearch.brand_footprint (quality uplift 3.1 — zero third-party
// footprint makes AI citation structurally impossible; mentions correlate
// ~3× stronger with AI visibility than backlinks) ---

type aisearchBrandFootprint struct{}

func (aisearchBrandFootprint) ID() core.CheckID          { return "aisearch.brand_footprint" }
func (aisearchBrandFootprint) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchBrandFootprint) Kind() CheckKind           { return KindDeterministic }
func (aisearchBrandFootprint) Requires() []core.Kind     { return []core.Kind{artifacts.KindMentions} }

func (aisearchBrandFootprint) Run(_ context.Context, in Input) (core.CheckResult, error) {
	mentions, _ := in.Bundle.Mentions()

	var earned float64
	switch {
	case mentions.ThirdPartyCount == 0:
		earned = 0
	case mentions.ThirdPartyCount <= 2:
		earned = 40
	case mentions.ThirdPartyCount <= 5:
		earned = 70
	default:
		earned = 100
	}

	var findings []core.Finding
	if mentions.ThirdPartyCount == 0 {
		findings = append(findings, core.Finding{
			CheckID:  "aisearch.brand_footprint",
			Severity: core.SeverityHigh,
			Title:    "No third-party footprint — AI engines have nothing to cite",
			Detail:   fmt.Sprintf("SERP queries for %s surfaced zero third-party domains mentioning the brand. AI answer engines cite entities other sources talk about; a brand that exists only on its own domain is structurally invisible to them.", strings.Join(mentions.QueriedTerms, " and ")),
			Recommendation: "Work the footprint in effort order: (1) declare existing profiles via Organization sameAs, " +
				"(2) get listed on directories and launch platforms relevant to your category, " +
				"(3) build a real community presence where your audience already is, " +
				"(4) establish a Wikidata entity once there are citable sources.",
			Falsifiability: "A repeat of the same SERP queries surfaces at least one third-party domain mentioning the brand.",
		})
	} else if len(mentions.KeySurfaces) == 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.brand_footprint",
			Severity:       core.SeverityLow,
			Title:          "Brand mentions exist, but none on key AI-cited surfaces",
			Detail:         fmt.Sprintf("%d third-party domain(s) mention the brand, but none of the surfaces AI engines cite most (Reddit, Product Hunt, G2, Wikipedia, GitHub, Hacker News, ...).", mentions.ThirdPartyCount),
			Recommendation: "Prioritize presence on the 2–3 key surfaces your category actually uses; one genuine launch/review thread beats ten directory stubs.",
			Falsifiability: "A repeat query shows the brand on at least one key surface.",
		})
	}

	// Cross-reference the sameAs declaration (opportunistic HTMLDeep read —
	// never in Requires, so a deep-pass failure can't skip this check).
	if deep, ok := in.Bundle.HTMLDeep(); ok && mentions.ThirdPartyCount > 0 {
		sameAs := 0
		for _, p := range deep.Pages {
			if p.OrganizationSameAsCount > sameAs {
				sameAs = p.OrganizationSameAsCount
			}
		}
		if sameAs == 0 {
			findings = append(findings, core.Finding{
				CheckID:        "aisearch.brand_footprint",
				Severity:       core.SeverityLow,
				Title:          "Third-party profiles exist but aren't declared via sameAs",
				Detail:         fmt.Sprintf("The brand is mentioned on %d third-party domain(s), yet the Organization schema declares zero sameAs links — the entity graph can't connect what you don't declare.", mentions.ThirdPartyCount),
				Recommendation: "List every owned/verified third-party profile in the Organization block's sameAs array.",
				Falsifiability: "The Organization schema carries sameAs entries pointing at the live third-party profiles.",
			})
		}
	}

	// Category occupancy: who owns the query this site exists to answer.
	// Requires a real result set — a thin SERP proves nothing. Gap-closure
	// round 4: the buyer-intent probe ("best ai audit software") carries real
	// weight — absence from the results buyers and AI engines ground on is a
	// Medium distribution gap that also caps the score, because a mention
	// count alone must never read as "AI engines have sources to cite" while
	// the site is invisible on its category's actual buying query. A
	// self-phrase probe (legacy or non-product site) stays a Low note:
	// self-coined terms rank trivially, so neither presence nor absence
	// proves much.
	if mentions.CategoryProbed && len(mentions.CategoryTopDomains) >= 5 && mentions.CategoryTargetPosition == 0 {
		occupants := mentions.CategoryTopDomains
		if len(occupants) > 5 {
			occupants = occupants[:5]
		}
		if mentions.CategoryQueryIntent == artifacts.CategoryIntentBuyer {
			if earned > 70 {
				earned = 70
			}
			findings = append(findings, core.Finding{
				CheckID:  "aisearch.brand_footprint",
				Severity: core.SeverityMedium,
				Title:    "Absent from the category query buyers actually search",
				Detail: fmt.Sprintf("A search for %q — the buying query for the category this site claims (%q) — surfaces %s, and this site nowhere in the probed results. AI answer engines ground \"what's the best X\" answers on exactly these results and the listicles inside them; a site absent from all of them is invisible at the moment of choice, whatever its own pages say.",
					mentions.CategoryQuery, mentions.CategorySourcePhrase, strings.Join(occupants, ", ")),
				Recommendation: "Work into the sources that own this query: pitch the non-competitor listicles ranking for it for inclusion, build the third-party review base (G2/Capterra with actual reviews), and publish the comparison content the occupying domains don't have.",
				Falsifiability: fmt.Sprintf("A repeat search for %q surfaces the site — or a listicle naming it — in the organic results.", mentions.CategoryQuery),
			})
		} else {
			findings = append(findings, core.Finding{
				CheckID:  "aisearch.brand_footprint",
				Severity: core.SeverityLow,
				Title:    "Competitors occupy the site's own category query",
				Detail: fmt.Sprintf("A search for %q — the category phrase this site's homepage claims — surfaces %s, and the site itself nowhere in the probed results. AI engines and searchers deciding who answers this category are being handed competitors every time.",
					mentions.CategoryQuery, strings.Join(occupants, ", ")),
				Recommendation: "Win the category query deliberately: a definitive category page targeting the exact phrase, third-party mentions that associate the brand with it, and comparison content the occupying domains don't have.",
				Falsifiability: fmt.Sprintf("A repeat search for %q surfaces the site in the organic results.", mentions.CategoryQuery),
			})
		}
	}

	evidence := map[string]any{
		"thirdPartyCount":   mentions.ThirdPartyCount,
		"thirdPartyDomains": mentions.ThirdPartyDomains,
	}
	if len(mentions.KeySurfaces) > 0 {
		evidence["keySurfaces"] = mentions.KeySurfaces
	}
	if mentions.CategoryProbed {
		evidence["categoryQuery"] = mentions.CategoryQuery
		evidence["categoryQueryIntent"] = mentions.CategoryQueryIntent
		evidence["categoryTopDomains"] = mentions.CategoryTopDomains
		evidence["categoryTargetPosition"] = mentions.CategoryTargetPosition
	}
	// Strength gate: without a buyer-intent category probe confirming the
	// site is even findable on its buying query, a raw mention count can't
	// honestly headline as "AI engines have sources to cite".
	if mentions.CategoryQueryIntent != artifacts.CategoryIntentBuyer || mentions.CategoryTargetPosition == 0 {
		evidence["strengthIneligible"] = true
	}
	return core.CheckResult{Score: fixedScore(earned), Findings: findings, Evidence: evidence}, nil
}
