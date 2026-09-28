package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

// Answer-block word band (fidelity must, scope §5): 134–167-word
// self-contained blocks are the citability sweet spot. The extraction lives
// in the deep-pass collector; these checks consume the counts.
const (
	answerBlockMinWords = 134
	answerBlockMaxWords = 167
)

// --- aisearch.answer_blocks (Citability 25) ---

type aisearchAnswerBlocks struct{}

func (aisearchAnswerBlocks) ID() core.CheckID          { return "aisearch.answer_blocks" }
func (aisearchAnswerBlocks) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchAnswerBlocks) Kind() CheckKind           { return KindDeterministic }
func (aisearchAnswerBlocks) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchAnswerBlocks) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	withBlocks, articles := 0, 0
	var missing []string
	for _, p := range pages {
		if p.IsHomepage {
			continue
		}
		articles++
		if p.AnswerBlockCount > 0 {
			withBlocks++
		} else {
			missing = append(missing, p.URL)
		}
	}

	var findings []core.Finding
	if len(missing) > 0 {
		findings = append(findings, core.Finding{
			CheckID:  "aisearch.answer_blocks",
			Severity: core.SeverityMedium,
			// The measurement is a word-count band, and the title must not
			// claim more: a 120-word answer in a list is perfectly citable.
			Title:          fmt.Sprintf("%d sampled page(s) without a paragraph in the %d–%d-word citation band", len(missing), answerBlockMinWords, answerBlockMaxWords),
			Detail:         fmt.Sprintf("AI answer engines most often lift self-contained passages of roughly %d–%d words; no paragraph on these pages falls in that band.", answerBlockMinWords, answerBlockMaxWords),
			Pages:          capPages(missing),
			Recommendation: "Open key sections with a direct, self-contained answer paragraph in the 134–167-word band before elaborating.",
			Falsifiability: fmt.Sprintf("Each listed page contains at least one standalone paragraph of %d–%d words that answers its section's question.", answerBlockMinWords, answerBlockMaxWords),
		})
	}

	return core.CheckResult{Score: ratioScore(withBlocks, articles), Findings: findings}, nil
}

// --- aisearch.structure (Structural 20) ---

type aisearchStructure struct{}

func (aisearchStructure) ID() core.CheckID          { return "aisearch.structure" }
func (aisearchStructure) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchStructure) Kind() CheckKind           { return KindDeterministic }
func (aisearchStructure) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchStructure) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	// Weighted 70/30 instead of conjunctive — a site with excellent
	// descriptive H2/H3 structure but zero question-form headings used to
	// score 0/100 while emitting only Low findings.
	earned, articles := 0.0, 0
	var flat, noQuestions []string
	for _, p := range pages {
		if p.IsHomepage {
			continue
		}
		articles++
		if len(p.Headings) >= 3 {
			earned += 70
		} else {
			flat = append(flat, p.URL)
		}
		if p.QuestionHeadings > 0 {
			earned += 30
		} else {
			noQuestions = append(noQuestions, p.URL)
		}
	}

	var findings []core.Finding
	if len(flat) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.structure",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) lack extractable heading structure", len(flat)),
			Detail:         "Fewer than three headings — AI extraction (and skimming) depends on a clear section skeleton.",
			Pages:          capPages(flat),
			Recommendation: "Structure articles with descriptive H2/H3 sections.",
			Falsifiability: "Each listed page renders at least three section headings.",
		})
	}
	if len(noQuestions) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.structure",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) use no question-form headings", len(noQuestions)),
			Detail:         "Question headings map directly onto how users prompt AI assistants and PAA queries.",
			Pages:          capPages(noQuestions),
			Recommendation: "Phrase key section headings as the questions readers actually ask.",
			Falsifiability: "Each listed page carries at least one question-form heading.",
		})
	}

	if articles == 0 {
		return core.CheckResult{Score: nil, Findings: findings}, nil
	}
	return core.CheckResult{Score: fixedScore(earned / float64(articles)), Findings: findings}, nil
}

// --- aisearch.authority_proxies (Authority 20 — decision 17 reassigned the
// Authority & Brand slice to detectable proxies: Organization sameAs, author
// bylines) ---

type aisearchAuthorityProxies struct{}

func (aisearchAuthorityProxies) ID() core.CheckID          { return "aisearch.authority_proxies" }
func (aisearchAuthorityProxies) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchAuthorityProxies) Kind() CheckKind           { return KindDeterministic }
func (aisearchAuthorityProxies) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchAuthorityProxies) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	hasOrg, sameAs := false, 0
	bylines, articles := 0, 0
	for _, p := range pages {
		if p.HasOrganizationSchema {
			hasOrg = true
		}
		if p.OrganizationSameAsCount > sameAs {
			sameAs = p.OrganizationSameAsCount
		}
		// Bylines are an article property — the same gate the content checks
		// use, so a nav-heavy sample can't zero this slice.
		if !isArticleLike(p) {
			continue
		}
		articles++
		if p.HasByline {
			bylines++
		}
	}
	// A Person entity with sameAs linkage (personal-brand sites) declares
	// "who is this site" just as an Organization block does.
	if !hasOrg && sameAs > 0 {
		hasOrg = true
	}

	earned := 0.0
	var findings []core.Finding
	if hasOrg {
		earned += 30
	} else {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.authority_proxies",
			Severity:       core.SeverityMedium,
			Title:          "No Organization or Person entity schema found",
			Detail:         "AI engines resolve \"who is this site\" through Organization/Person markup; none was found on the sample.",
			Recommendation: "Add Organization JSON-LD (name, logo, url) sitewide — or Person markup for a personal brand.",
			Falsifiability: "The homepage carries a parsed Organization (or Person) block in the Rich Results Test.",
		})
	}
	if sameAs >= 2 {
		earned += 30
	} else if hasOrg {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.authority_proxies",
			Severity:       core.SeverityLow,
			Title:          "Organization schema has little or no sameAs linkage",
			Detail:         "sameAs ties the entity to its profiles (LinkedIn, Crunchbase, socials) — the cheapest entity-disambiguation signal there is.",
			Recommendation: "Add 2+ sameAs URLs to the Organization block.",
			Snippet:        buildSameAsSnippet(in),
			Falsifiability: "The Organization block lists at least two live sameAs profile URLs.",
		})
	}
	earned += share(bylines, articles) * 40
	if articles > 0 && bylines == 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.authority_proxies",
			Severity:       core.SeverityLow,
			Title:          "No author identity on sampled content",
			Detail:         "Author bylines are the per-page authority proxy AI engines can actually verify.",
			Recommendation: "Byline every article and mark the author up in schema.",
			Falsifiability: "Sampled articles carry visible bylines mirrored in their schema author fields.",
		})
	}

	return core.CheckResult{Score: fixedScore(earned), Findings: findings}, nil
}

// sameAsSurfaceTemplates maps a mentions key surface to a profile-URL
// template — the exact-artifact seed for the sameAs snippet. Only surfaces
// where a company profile is the norm.
var sameAsSurfaceTemplates = map[string]string{
	"linkedin.com":    "https://www.linkedin.com/company/<your-company>",
	"ycombinator.com": "https://www.ycombinator.com/companies/<your-company>",
	"github.com":      "https://github.com/<your-org>",
	"producthunt.com": "https://www.producthunt.com/products/<your-product>",
	"g2.com":          "https://www.g2.com/products/<your-product>",
	"wikidata.org":    "https://www.wikidata.org/wiki/<your-entity-id>",
}

// buildSameAsSnippet assembles the exact sameAs JSON artifact, seeded from
// the surfaces the mentions collector actually saw the brand on (read
// opportunistically); generic placeholders otherwise.
func buildSameAsSnippet(in Input) string {
	var urls []string
	if mentions, ok := in.Bundle.Mentions(); ok {
		// Deterministic order: iterate the template keys sorted.
		var surfaces []string
		for s := range sameAsSurfaceTemplates {
			surfaces = append(surfaces, s)
		}
		sort.Strings(surfaces)
		for _, s := range surfaces {
			if mentions.KeySurfaces[s] {
				urls = append(urls, sameAsSurfaceTemplates[s])
			}
		}
	}
	if len(urls) < 2 {
		urls = []string{
			"https://www.linkedin.com/company/<your-company>",
			"https://x.com/<your-handle>",
			"https://www.crunchbase.com/organization/<your-company>",
		}
	}
	var b strings.Builder
	b.WriteString("\"sameAs\": [\n")
	for i, u := range urls {
		b.WriteString("  \"" + u + "\"")
		if i < len(urls)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]")
	return b.String()
}

// --- aisearch.crawler_access (TechAccess 15 of the 20 slice; llms.txt is
// presence-only with ZERO ranking weight — fidelity must) ---

type aisearchCrawlerAccess struct{}

func (aisearchCrawlerAccess) ID() core.CheckID          { return "aisearch.crawler_access" }
func (aisearchCrawlerAccess) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchCrawlerAccess) Kind() CheckKind           { return KindDeterministic }
func (aisearchCrawlerAccess) Requires() []core.Kind {
	return []core.Kind{artifacts.KindCrawl, artifacts.KindHTMLDeep}
}

func (aisearchCrawlerAccess) Run(_ context.Context, in Input) (core.CheckResult, error) {
	deep, _ := in.Bundle.HTMLDeep()
	robots := deep.Robots

	var blocked []string
	total := 0
	for agent, allowed := range robots.Crawlers {
		total++
		if !allowed {
			blocked = append(blocked, agent)
		}
	}
	sort.Strings(blocked) // map order must never decide finding text

	var findings []core.Finding
	if len(blocked) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.crawler_access",
			Severity:       core.SeverityMedium,
			Title:          fmt.Sprintf("robots.txt blocks %d AI crawler(s)", len(blocked)),
			Detail:         fmt.Sprintf("Blocked: %v. Blocking AI crawlers is a legitimate policy choice — but it removes the site from those engines' answers, so it must be deliberate.", blocked),
			Recommendation: "If AI-search visibility is wanted, allow these user-agents in robots.txt; if the block is policy, keep it and accept the trade.",
			Falsifiability: "robots.txt no longer disallows the listed user-agents (or the block is documented as intentional).",
		})
	} else if robots.Found && !robots.HasExplicitAIRules {
		// Everything is allowed, but only implicitly through the * default.
		// Explicit groups document the intent and keep a future blanket
		// Disallow from silently cutting off AI search. Zero score weight.
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.crawler_access",
			Severity:       core.SeverityInfo,
			Title:          "AI crawlers are allowed only implicitly (no explicit robots.txt rules)",
			Detail:         "Nothing is broken — AI crawlers ride the User-agent: * default today. Explicit per-crawler rules document the policy and prevent a future blanket Disallow from silently removing the site from AI answers.",
			Recommendation: "Add explicit Allow groups for the AI crawlers you want (and keep them in sync with policy).",
			Snippet: "User-agent: GPTBot\nAllow: /\n\n" +
				"User-agent: OAI-SearchBot\nAllow: /\n\n" +
				"User-agent: ClaudeBot\nAllow: /\n\n" +
				"User-agent: PerplexityBot\nAllow: /\n\n" +
				"User-agent: Google-Extended\nAllow: /",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}
	// llms.txt: reported with depth signals, never scored (evidence-based
	// posture: no proven ranking weight — we refuse to pretend otherwise).
	// The evidence key feeds the report's Strengths section when coverage is
	// full.
	evidence := map[string]any{}
	switch {
	case !deep.LlmsTxtFound:
		evidence["llmsTxt"] = "missing"
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.crawler_access",
			Severity:       core.SeverityInfo,
			Title:          "No llms.txt present",
			Detail:         "llms.txt is an emerging convention with NO demonstrated ranking or citation weight — reported for completeness only, and it carries zero points in this score.",
			Recommendation: "Optional: publish /llms.txt describing key content for LLM agents. Skipping it costs nothing today.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	case deep.LlmsTxt.Found && !(deep.LlmsTxt.HasHomepageLink || deep.LlmsTxt.LinksAboutOrProduct):
		evidence["llmsTxt"] = "content_only"
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.crawler_access",
			Severity:       core.SeverityInfo,
			Title:          "llms.txt describes your content but not your product/company",
			Detail:         fmt.Sprintf("The file lists %d link(s) but never points an LLM at the homepage/about/product layer — an agent reading it learns what you write about, not what you are. (Still zero score weight — llms.txt has no proven ranking effect.)", deep.LlmsTxt.LinkCount),
			Recommendation: "Add a short section linking the homepage, product/features, and about pages with one-line descriptions.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	default:
		// Present and covering the homepage/about layer (or a legacy artifact
		// without depth data) — nothing to flag; full coverage surfaces as a
		// strength. One remaining depth nudge: a file that links pages but
		// states no plain facts makes an agent fetch to learn what the
		// product even is. (Still zero score weight — fidelity must.)
		if deep.LlmsTxt.Found {
			evidence["llmsTxt"] = "covered"
			if !deep.LlmsTxt.HasKeyFacts {
				findings = append(findings, core.Finding{
					CheckID:        "aisearch.crawler_access",
					Severity:       core.SeverityInfo,
					Title:          "llms.txt links your pages but states no key facts",
					Detail:         "The file is a link index: an LLM reading it must fetch pages to learn what the product is, what it costs, and who it's for. A short factual block makes those answers quotable directly — keep every number identical to the one on the corresponding page. (Zero score weight — llms.txt has no proven ranking effect.)",
					Recommendation: "Add a \"## Key facts\" section of plain, verifiable statements (what the product is, pricing model, operator, primary market).",
					Snippet: "## Key facts\n" +
						"- <Product> is a <one-line category statement>.\n" +
						"- <The single most important usage/scale fact, stated with the same number the site uses>.\n" +
						"- <Pricing model: who pays, who doesn't>.\n" +
						"- Operated by <legal entity, city, country>.\n" +
						"- Primary market: <market>. Language: <language>.",
					Falsifiability: "This is informational; presence or absence changes no score.",
				})
			}
		} else {
			evidence["llmsTxt"] = "present"
		}
	}
	if deep.LlmsFullTxtFound {
		evidence["llmsFullTxt"] = "present"
	}

	allowedCount := total - len(blocked)
	return core.CheckResult{Score: ratioScore(allowedCount, total), Findings: findings, Evidence: evidence}, nil
}

// --- aisearch.agent_ux (5 — semantic-HTML operability for AI agents,
// decision 11) ---

type aisearchAgentUX struct{}

func (aisearchAgentUX) ID() core.CheckID          { return "aisearch.agent_ux" }
func (aisearchAgentUX) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchAgentUX) Kind() CheckKind           { return KindDeterministic }
func (aisearchAgentUX) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchAgentUX) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	passing := 0
	var divSoup []string
	for _, p := range pages {
		score := 0
		if p.HasMain {
			score++
		}
		if p.HasNav {
			score++
		}
		if p.HasArticleTag || p.IsHomepage {
			score++
		}
		if p.DivRatio < 0.75 {
			score++
		}
		if score >= 3 {
			passing++
		} else {
			divSoup = append(divSoup, p.URL)
		}
	}

	var findings []core.Finding
	if len(divSoup) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.agent_ux",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) are hard for agents to operate", len(divSoup)),
			Detail:         "Missing landmark elements (main/nav/article) or div-soup markup makes pages hard for AI agents and assistive tech to parse and act on.",
			Pages:          capPages(divSoup),
			Recommendation: "Use semantic landmarks (header/nav/main/article/footer) and real buttons/labels over styled divs.",
			Falsifiability: "Each listed page exposes main and nav landmarks and its div/span share of elements drops below 75%.",
		})
	}

	return core.CheckResult{Score: ratioScore(passing, len(pages)), Findings: findings}, nil
}

// --- aisearch.multimodal (Multi-modal 15) ---

type aisearchMultimodal struct{}

func (aisearchMultimodal) ID() core.CheckID          { return "aisearch.multimodal" }
func (aisearchMultimodal) Category() core.CategoryID { return core.CategoryAISearch }
func (aisearchMultimodal) Kind() CheckKind           { return KindDeterministic }
func (aisearchMultimodal) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

func (aisearchMultimodal) Run(_ context.Context, in Input) (core.CheckResult, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		// No page ever fetched (all blocked) — not assessed, never "healthy".
		return core.CheckResult{Score: nil}, nil
	}

	rich, articles := 0, 0
	var textOnly []string
	var bareHomepage string
	anyVideo := false
	for _, p := range pages {
		if p.VideoEmbedCount > 0 {
			anyVideo = true
		}
		if p.IsHomepage {
			// The homepage doesn't feed the article score, but a homepage with
			// zero real images and no video is its own finding: nothing to
			// show in Google Images, no visual proof of the product.
			if len(p.Images) == 0 && p.VideoEmbedCount == 0 {
				bareHomepage = p.URL
			}
			continue
		}
		articles++
		modalities := 0
		if len(p.Images) > 0 {
			modalities++
		}
		if p.ListCount > 0 || p.TableCount > 0 {
			modalities++
		}
		if p.VideoEmbedCount > 0 {
			modalities++
		}
		if modalities >= 2 {
			rich++
		} else {
			textOnly = append(textOnly, p.URL)
		}
	}

	var findings []core.Finding
	if bareHomepage != "" {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.multimodal",
			Severity:       core.SeverityMedium,
			Title:          "Homepage carries no real content images and no video",
			Detail:         "The homepage's content area renders no indexable <img> elements and embeds no video (inline SVG decoration and data-URI graphics don't count). That means no Google Images presence, no visual for social/AI answer cards, and — for a product — no screenshot-level proof of what it actually looks like in use.",
			Pages:          []string{bareHomepage},
			Recommendation: "Add real product screenshots (with descriptive alt text) and ideally a short demo video to the homepage; serve them as sized <img> elements so they're indexable.",
			Falsifiability: "The homepage renders at least one <img> with a descriptive alt attribute.",
		})
	}
	if len(pages) > 0 && !anyVideo {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.multimodal",
			Severity:       core.SeverityInfo,
			Title:          "No video anywhere in the sample",
			Detail:         "None of the sampled pages embeds a video. Pages combining text with images and video surface in more result formats, and one short demo doubles as a launch/social/community asset.",
			Recommendation: "Record one 45–60s product demo, host it on YouTube, embed it with a click-to-load facade, and mark it up with VideoObject schema.",
			Falsifiability: "This is informational; presence or absence changes no score.",
		})
	}
	if len(textOnly) > 0 {
		findings = append(findings, core.Finding{
			CheckID:        "aisearch.multimodal",
			Severity:       core.SeverityLow,
			Title:          fmt.Sprintf("%d sampled page(s) carry fewer than two content modalities", len(textOnly)),
			Detail:         "Pages that are mostly prose (at most one of: images, lists/tables, video) surface in fewer result formats and fewer AI answer shapes.",
			Pages:          capPages(textOnly),
			Recommendation: "Add at least two supporting modalities per article: relevant images, structured lists/tables, or an embedded video.",
			Falsifiability: "Each listed page renders at least two of: images, lists/tables, video.",
		})
	}

	return core.CheckResult{Score: ratioScore(rich, articles), Findings: findings}, nil
}
