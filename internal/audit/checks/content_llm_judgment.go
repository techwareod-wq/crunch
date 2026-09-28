package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/template"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	auditdto "github.com/atharva-ng/crunch/internal/audit/dto"
	"github.com/atharva-ng/crunch/internal/audit/prompts"
)

// contentLLMJudgment is THE V1 LLM check (decision 17): one Anthropic call
// judging expertise/experience/originality — the quality slice no threshold
// can measure. The judge stage owns the API call, model selection, retries
// and token tracking; this check owns only prompt construction and parsing
// (Template Method). Its 50-point allocation carries the E-E-A-T sub-weights
// Trust 30 / Expertise 25 / Authority 25 / Experience 20 in-prompt.
type contentLLMJudgment struct{}

func (contentLLMJudgment) ID() core.CheckID          { return "content.llm_judgment" }
func (contentLLMJudgment) Category() core.CategoryID { return core.CategoryContentEEAT }
func (contentLLMJudgment) Kind() CheckKind           { return KindLLMJudgment }
func (contentLLMJudgment) Requires() []core.Kind     { return []core.Kind{artifacts.KindHTMLDeep} }

// Run never executes for an LLM check — the judge stage drives
// BuildPrompt/ParseResult instead. Returning an error keeps a miswired
// engine loudly broken rather than silently scoring.
func (contentLLMJudgment) Run(context.Context, Input) (core.CheckResult, error) {
	return core.CheckResult{}, fmt.Errorf("content.llm_judgment must run via the judge stage, not the deterministic engine")
}

// judgmentExcerptChars caps one page's text in the prompt.
const judgmentExcerptChars = 4000

// judgmentSnippetMaxChars caps one finding's exact-artifact snippet — an
// over-generous model doesn't get to bloat the report doc.
const judgmentSnippetMaxChars = 1500

func (contentLLMJudgment) BuildPrompt(in Input, evidence map[core.CheckID]map[string]any) (string, error) {
	pages := fetchedDeepPages(in)
	if len(pages) == 0 {
		return "", fmt.Errorf("no fetched deep-pass pages to judge")
	}

	var promptPages []auditdto.ContentJudgmentPage
	for _, p := range pages {
		excerpt := p.ContentExcerpt
		if len(excerpt) > judgmentExcerptChars {
			excerpt = excerpt[:judgmentExcerptChars]
		}
		if excerpt == "" {
			continue
		}
		promptPages = append(promptPages, auditdto.ContentJudgmentPage{
			URL:     p.URL,
			Title:   p.Title,
			Excerpt: excerpt,
		})
	}
	if len(promptPages) == 0 {
		return "", fmt.Errorf("deep-pass pages carry no content excerpts to judge")
	}

	evidenceJSON, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal judgment evidence: %w", err)
	}

	tmpl, err := template.New("content_judgment").Parse(prompts.ContentJudgment)
	if err != nil {
		return "", fmt.Errorf("parse content judgment template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, auditdto.ContentJudgmentPrompt{
		Domain:       in.Run.TargetDomain,
		PageCount:    len(promptPages),
		Pages:        promptPages,
		EvidenceJSON: string(evidenceJSON),
		SiteContext:  buildJudgmentSiteContext(in),
	}); err != nil {
		return "", fmt.Errorf("execute content judgment template: %w", err)
	}
	return buf.String(), nil
}

// buildJudgmentSiteContext assembles the strategy section's raw material
// (quality uplift 4.2). Every source is read opportunistically — a missing
// artifact just leaves its block out of the prompt.
func buildJudgmentSiteContext(in Input) auditdto.ContentJudgmentSiteContext {
	sc := auditdto.ContentJudgmentSiteContext{}
	if deep, ok := in.Bundle.HTMLDeep(); ok {
		sc.DetectedTech = deep.DetectedTech
		sc.EarliestContentDate = deep.EarliestContentDate
		// Schema + llms.txt state, so the strategy section reasons from what
		// the deterministic layer measured instead of guessing at absence.
		typeSeen := map[string]bool{}
		for _, p := range deep.Pages {
			if !p.Fetched {
				continue
			}
			pageHasInvalid := false
			for _, block := range p.SchemaBlocks {
				if !block.Valid {
					sc.InvalidSchemaBlockCount++
					pageHasInvalid = true
				}
				for _, t := range block.Types {
					if !typeSeen[t] && len(sc.SchemaTypesPresent) < 16 {
						typeSeen[t] = true
						sc.SchemaTypesPresent = append(sc.SchemaTypesPresent, t)
					}
				}
			}
			if pageHasInvalid && len(sc.InvalidSchemaPages) < 8 {
				sc.InvalidSchemaPages = append(sc.InvalidSchemaPages, p.URL)
			}
			if p.OrganizationSameAsCount > sc.OrganizationSameAsCount {
				sc.OrganizationSameAsCount = p.OrganizationSameAsCount
			}
		}
		sc.HasLlmsTxt = deep.LlmsTxtFound
		sc.LlmsTxtHasKeyFacts = deep.LlmsTxt.HasKeyFacts
	}
	if auth, ok := in.Bundle.Authority(); ok {
		sc.DomainRating = auth.DomainRating
		sc.ReferringDomains = auth.ReferringDomains
		sc.KeywordsCount = auth.KeywordsCount
	}
	if mentions, ok := in.Bundle.Mentions(); ok {
		sc.HasMentionsData = true
		sc.ThirdPartyCount = mentions.ThirdPartyCount
		domains := mentions.ThirdPartyDomains
		if len(domains) > 10 {
			domains = domains[:10]
		}
		sc.ThirdPartyDomains = domains
		if mentions.CategoryProbed {
			sc.CategoryQuery = mentions.CategoryQuery
			sc.CategoryTopDomains = mentions.CategoryTopDomains
			sc.CategoryTargetPosition = mentions.CategoryTargetPosition
		}
	}
	// Content inventory (crawl-wide, not just the deep sample) — the raw
	// material for concrete topic-cluster/hub proposals.
	if crawl, ok := in.Bundle.Crawl(); ok {
		for _, p := range crawl.Pages {
			if p.StatusCode != 200 || p.IsRedirect || p.WordCount < 300 || p.Title == "" {
				continue
			}
			if u, err := url.Parse(p.URL); err == nil && strings.Trim(u.Path, "/") != "" {
				if len(sc.ContentPageTitles) < judgmentContentTitleCap {
					sc.ContentPageTitles = append(sc.ContentPageTitles, fmt.Sprintf("%s — %s", p.Title, u.Path))
				}
			}
		}
	}
	// Searcher questions from the SXO SERPs (capped, deduped).
	if serp, ok := in.Bundle.SERP(); ok {
		seen := map[string]bool{}
		for _, sp := range serp.Pages {
			for _, q := range sp.PAAQuestions {
				lower := strings.ToLower(q)
				if !seen[lower] && len(sc.PAAQuestions) < judgmentPAACap {
					seen[lower] = true
					sc.PAAQuestions = append(sc.PAAQuestions, q)
				}
			}
		}
	}
	return sc
}

// judgmentContentTitleCap / judgmentPAACap bound the judgment prompt's
// inventory sections.
const (
	judgmentContentTitleCap = 30
	judgmentPAACap          = 12
)

func (contentLLMJudgment) ParseResult(raw string) (core.CheckResult, error) {
	var parsed auditdto.ContentJudgmentResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return core.CheckResult{}, fmt.Errorf("unmarshal content judgment response: %w", err)
	}
	if parsed.Score < 0 || parsed.Score > 100 {
		return core.CheckResult{}, fmt.Errorf("content judgment score %d out of range", parsed.Score)
	}

	validSeverity := map[string]core.Severity{
		"critical": core.SeverityCritical,
		"high":     core.SeverityHigh,
		"medium":   core.SeverityMedium,
		"low":      core.SeverityLow,
		"info":     core.SeverityInfo,
	}
	var findings []core.Finding
	for _, f := range parsed.Findings {
		sev, ok := validSeverity[f.Severity]
		if !ok {
			sev = core.SeverityInfo // an invented severity degrades, never breaks
		}
		if f.Title == "" {
			continue
		}
		falsifiability := f.Falsifiability
		if falsifiability == "" {
			// Falsifiability is mandatory IP — an LLM omission gets a
			// serviceable default rather than dropping the finding.
			falsifiability = "Re-run the audit after the change: this finding no longer appears."
		}
		snippet := f.Snippet
		if len(snippet) > judgmentSnippetMaxChars {
			snippet = snippet[:judgmentSnippetMaxChars]
		}
		findings = append(findings, core.Finding{
			CheckID:        "content.llm_judgment",
			Severity:       sev,
			Title:          f.Title,
			Detail:         f.Detail,
			Pages:          capPages(f.Pages),
			Recommendation: f.Recommendation,
			Snippet:        snippet,
			Falsifiability: falsifiability,
		})
	}

	evidence := map[string]any{
		"subScores": parsed.SubScores,
	}
	// Strategy items ride in Evidence (quality uplift 4.2); the synthesize
	// stage lifts them into report.strategic_opportunities. They are NOT
	// findings — no severity, no falsifiability contract, zero score impact.
	if len(parsed.Strategy) > 0 {
		var strategy []map[string]any
		for _, s := range parsed.Strategy {
			if s.Title == "" {
				continue
			}
			strategy = append(strategy, map[string]any{
				"title":     s.Title,
				"detail":    s.Detail,
				"rationale": s.Rationale,
				"plays":     s.Plays,
			})
		}
		if len(strategy) > 0 {
			evidence["strategy"] = strategy
		}
	}

	return core.CheckResult{
		Score:    fixedScore(float64(parsed.Score)),
		Findings: findings,
		Evidence: evidence,
	}, nil
}
