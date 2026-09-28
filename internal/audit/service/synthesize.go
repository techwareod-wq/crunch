package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"text/template"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	auditdto "github.com/atharva-ng/crunch/internal/audit/dto"
	"github.com/atharva-ng/crunch/internal/audit/engine"
	"github.com/atharva-ng/crunch/internal/audit/prompts"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// narrativeTopFindings caps what the narrative call reads — exec summary
// over the top of the list, never the whole report (decision 17).
const narrativeTopFindings = 12

// HandleSynthesize is the terminal stage: aggregate scores per the frozen
// snapshot, code-bucket the action plan, layer the one small narrative LLM
// call on top (soft-fail: narrative = nil, report completes — decision 17),
// assemble the Domain Overview strip, and embed the report.
func (s *auditService) HandleSynthesize(ctx context.Context, userID string, p audit.AuditRunPayload) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit synthesize: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit synthesize: run not found: %s", p.RunID)
	}
	if run.Status != models.AuditStatusSynthesizing {
		return nil
	}

	// All outcomes: deterministic + the judgment (when it ran).
	outcomes := append([]models.AuditCheckOutcome{}, run.CheckOutcomes...)
	if run.JudgmentOutcome != nil {
		outcomes = append(outcomes, *run.JudgmentOutcome)
	}

	var aggInputs []engine.AggregateInput
	findingsByCategory := map[core.CategoryID][]core.Finding{}
	var allFindings []core.Finding
	for _, o := range outcomes {
		aggInputs = append(aggInputs, engine.AggregateInput{
			CheckID:  core.CheckID(o.CheckID),
			Category: core.CategoryID(o.Category),
			Skipped:  o.Skipped,
			HasScore: o.HasScore,
			Earned:   o.Earned,
			Possible: o.Possible,
		})
		cat := core.CategoryID(o.Category)
		findingsByCategory[cat] = append(findingsByCategory[cat], o.Findings...)
		allFindings = append(allFindings, o.Findings...)
	}

	agg := engine.Aggregate(aggInputs, run.SpecSnapshot)

	var categories []models.CategoryReport
	for _, c := range agg.Categories {
		categories = append(categories, models.CategoryReport{
			ID:       string(c.ID),
			Label:    engine.CategoryLabel(run.SpecSnapshot, c.ID),
			Weight:   run.SpecSnapshot.Weights[c.ID],
			Scored:   c.Scored,
			Score:    c.Score,
			Reason:   c.Reason,
			Findings: findingsByCategory[c.ID],
		})
	}

	// Bundle loads BEFORE the narrative call now — the narrative reads stack
	// + site-age context off it (quality uplift 4.3).
	bundle, err := s.loadBundle(ctx, run)
	if err != nil {
		log.Warn("audit synthesize: bundle reload failed, report omits overview detail", "runId", p.RunID, "error", err)
		bundle = nil
	}

	strengths := engine.BuildStrengths(outcomes, run.SpecSnapshot)

	plan := engine.BuildActionPlan(allFindings)
	if narrative := s.buildNarrative(ctx, run, agg, allFindings, bundle, strengths); narrative != "" {
		plan.Narrative = &narrative
	}

	summary := buildSummary(agg, allFindings)
	if bundle != nil {
		if crawl, ok := bundle.Crawl(); ok {
			summary.PagesCrawled = crawl.PagesCrawled
		}
	}

	report := &models.AuditReportDoc{
		Summary:                summary,
		DomainOverview:         s.buildDomainOverview(bundle),
		Strengths:              strengths,
		Categories:             categories,
		ActionPlan:             plan,
		StrategicOpportunities: decodeStrategicOpportunities(run),
		Constraints:            run.Constraints,
	}
	report.Drift = buildDriftSummary(ctx, run, report)

	now := time.Now()
	won, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
		[]int{models.AuditStatusSynthesizing}, models.AuditStatusComplete,
		bson.M{"report": report, "completed_at": now})
	if err != nil {
		return fmt.Errorf("audit synthesize: complete run: %w", err)
	}
	if !won {
		return nil
	}
	log.Info("audit run complete", "runId", p.RunID, "domain", run.TargetDomain, "score", report.Summary.OverallScore)
	return nil
}

// driftTitleCap bounds the resolved/new title lists persisted per report —
// the counts carry the full truth.
const driftTitleCap = 8

// buildDriftSummary compares this report against the target's previous
// completed run. Best-effort: any failure (or a first audit) yields nil and
// the report ships without a drift block. Info-severity findings are excluded
// from the new/resolved diff — informational nudges appearing or vanishing
// isn't drift a user should act on.
func buildDriftSummary(ctx context.Context, run *models.AuditRun, report *models.AuditReportDoc) *models.DriftSummary {
	found, prev, err := models.FindPreviousCompletedAuditRun(ctx, run)
	if err != nil {
		log.Warn("audit synthesize: previous-run lookup failed, report omits drift", "runId", run.ID.Hex(), "error", err)
		return nil
	}
	if !found || prev.Report == nil || prev.CompletedAt == nil {
		return nil
	}
	return diffAuditReports(prev, report)
}

// diffAuditReports is the pure comparison half of the drift block.
func diffAuditReports(prev *models.AuditRun, report *models.AuditReportDoc) *models.DriftSummary {
	drift := &models.DriftSummary{
		PreviousRunID:       prev.ID,
		PreviousCompletedAt: *prev.CompletedAt,
		PreviousScore:       prev.Report.Summary.OverallScore,
		CurrentScore:        report.Summary.OverallScore,
		OverallDelta:        report.Summary.OverallScore - prev.Report.Summary.OverallScore,
	}

	prevCat := map[string]models.CategoryReport{}
	for _, c := range prev.Report.Categories {
		prevCat[c.ID] = c
	}
	for _, c := range report.Categories {
		p, ok := prevCat[c.ID]
		if !ok || !ok2(c.Scored, p.Scored) {
			continue // a category unscored on either side has no honest delta
		}
		drift.CategoryDeltas = append(drift.CategoryDeltas, models.DriftCategoryDelta{
			ID: c.ID, Label: c.Label,
			Previous: p.Score, Current: c.Score, Delta: c.Score - p.Score,
		})
	}

	// Finding diff by (check_id, count-normalized title), info severity
	// excluded. Titles embed counts ("53 page(s) with…"), so a raw-title key
	// turned every partial fix into one "resolved" plus one brand-new issue —
	// digits normalize to '#' so the finding class carries the identity.
	key := func(f core.Finding) string {
		return string(f.CheckID) + "\x00" + driftDigitRe.ReplaceAllString(f.Title, "#")
	}
	actionable := func(f core.Finding) bool { return f.Severity != core.SeverityInfo }
	collect := func(cats []models.CategoryReport) map[string]string {
		out := map[string]string{}
		for _, c := range cats {
			for _, f := range c.Findings {
				if actionable(f) {
					out[key(f)] = f.Title
				}
			}
		}
		return out
	}
	prevFindings := collect(prev.Report.Categories)
	currFindings := collect(report.Categories)
	for k, title := range prevFindings {
		if _, still := currFindings[k]; !still {
			drift.ResolvedCount++
			if len(drift.ResolvedTitles) < driftTitleCap {
				drift.ResolvedTitles = append(drift.ResolvedTitles, title)
			}
		}
	}
	for k, title := range currFindings {
		if _, was := prevFindings[k]; !was {
			drift.NewCount++
			if len(drift.NewTitles) < driftTitleCap {
				drift.NewTitles = append(drift.NewTitles, title)
			}
		}
	}
	sort.Strings(drift.ResolvedTitles)
	sort.Strings(drift.NewTitles)
	return drift
}

// ok2 reports both category sides carrying a real score.
func ok2(a, b bool) bool { return a && b }

// driftDigitRe normalizes counts out of finding titles for drift identity.
var driftDigitRe = regexp.MustCompile(`\d+`)

func buildSummary(agg engine.AggregateResult, findings []core.Finding) models.AuditSummary {
	counts := map[string]int{}
	for _, f := range findings {
		counts[string(f.Severity)]++
	}
	var unscored []string
	for _, id := range agg.Unscored {
		unscored = append(unscored, string(id))
	}
	return models.AuditSummary{
		OverallScore:       agg.Overall,
		UnscoredCategories: unscored,
		FindingCounts:      counts,
	}
}

// buildDomainOverview assembles the unscored context strip (decision 8) from
// the authority + crawl artifacts. Nil when authority data never landed —
// the FE simply omits the strip.
func (s *auditService) buildDomainOverview(bundle *artifacts.Bundle) *models.DomainOverview {
	if bundle == nil {
		return nil
	}
	auth, ok := bundle.Authority()
	if !ok {
		return nil
	}
	overview := &models.DomainOverview{
		DomainRating:     auth.DomainRating,
		LocationCode:     auth.LocationCode,
		LocationName:     s.locations.NameForCode(auth.LocationCode),
		KeywordsCount:    auth.KeywordsCount,
		OrganicETV:       auth.OrganicETV,
		Pos1:             auth.Pos1,
		Pos2_3:           auth.Pos2_3,
		Pos4_10:          auth.Pos4_10,
		Pos11_20:         auth.Pos11_20,
		Pos21Plus:        auth.Pos21_30 + auth.Pos31Plus,
		ReferringDomains: auth.ReferringDomains,
		Backlinks:        auth.Backlinks,
	}
	if crawl, ok := bundle.Crawl(); ok {
		overview.OnPageScore = crawl.OnPageScore
	}
	return overview
}

// buildNarrative runs the one short narrative call. Every failure path
// returns "" — the report completes without it. Bundle and strengths are
// context only (quality uplift 4.3) and may be nil/empty.
func (s *auditService) buildNarrative(ctx context.Context, run *models.AuditRun, agg engine.AggregateResult,
	findings []core.Finding, bundle *artifacts.Bundle, strengths []models.StrengthItem) string {
	if s.llm == nil || s.llm.Anthropic == nil {
		return ""
	}
	top := engine.TopFindings(findings, narrativeTopFindings)
	if len(top) == 0 {
		return ""
	}
	var promptFindings []auditdto.NarrativeFinding
	for _, f := range top {
		promptFindings = append(promptFindings, auditdto.NarrativeFinding{
			Severity: string(f.Severity),
			Category: categoryOfCheck(run, f.CheckID),
			Title:    f.Title,
			Detail:   f.Detail,
		})
	}

	prompt := auditdto.NarrativePrompt{
		Domain:       run.TargetDomain,
		OverallScore: agg.Overall,
		Findings:     promptFindings,
	}
	for _, c := range agg.Categories {
		prompt.CategoryScores = append(prompt.CategoryScores, auditdto.NarrativeCategoryScore{
			Label:  engine.CategoryLabel(run.SpecSnapshot, c.ID),
			Score:  c.Score,
			Scored: c.Scored,
		})
	}
	if bundle != nil {
		if deep, ok := bundle.HTMLDeep(); ok {
			prompt.DetectedTech = deep.DetectedTech
			prompt.EarliestContentDate = deep.EarliestContentDate
		}
	}
	for _, st := range strengths {
		prompt.Strengths = append(prompt.Strengths, st.Text)
	}

	tmpl, err := template.New("narrative").Parse(prompts.Narrative)
	if err != nil {
		log.Error("audit synthesize: narrative template parse failed", "error", err)
		return ""
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, prompt); err != nil {
		log.Error("audit synthesize: narrative template execute failed", "error", err)
		return ""
	}

	maxTokens := s.values.LLM.NarrativeMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	model := s.values.LLM.NarrativeModel
	if model == "" {
		model = dto.AnthropicSonnet5
	}
	resp, err := s.llm.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: dto.RoleUser, Content: buf.String()}},
		Model:     model,
		MaxTokens: maxTokens,
	})
	if err != nil {
		log.Warn("audit synthesize: narrative call failed, completing without it", "runId", run.ID.Hex(), "error", err)
		return ""
	}
	cleaned, err := s.llm.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return ""
	}
	var parsed auditdto.NarrativeResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		log.Warn("audit synthesize: narrative parse failed, completing without it", "runId", run.ID.Hex(), "error", err)
		return ""
	}
	return parsed.Narrative
}

// decodeStrategicOpportunities lifts the judgment call's strategy items out
// of the persisted evidence (quality uplift 4.2). The evidence round-trips
// through Mongo between the judge and synthesize stages, so a bson re-marshal
// is the robust decode; any failure degrades to an absent section.
func decodeStrategicOpportunities(run *models.AuditRun) []models.StrategicOpportunity {
	if run.JudgmentOutcome == nil {
		return nil
	}
	raw, ok := run.JudgmentOutcome.Evidence["strategy"]
	if !ok {
		return nil
	}
	payload, err := bson.Marshal(bson.M{"strategy": raw})
	if err != nil {
		log.Warn("audit synthesize: strategy re-marshal failed", "runId", run.ID.Hex(), "error", err)
		return nil
	}
	var decoded struct {
		Strategy []models.StrategicOpportunity `bson:"strategy"`
	}
	if err := bson.Unmarshal(payload, &decoded); err != nil {
		log.Warn("audit synthesize: strategy decode failed", "runId", run.ID.Hex(), "error", err)
		return nil
	}
	// Cap at 3 — the prompt asks for 2–3; an over-generous model doesn't get
	// to bloat the report.
	if len(decoded.Strategy) > 3 {
		decoded.Strategy = decoded.Strategy[:3]
	}
	return decoded.Strategy
}

// categoryOfCheck resolves a finding's category from the run's outcomes
// (used only for narrative context — a miss degrades to empty).
func categoryOfCheck(run *models.AuditRun, id core.CheckID) string {
	for _, o := range run.CheckOutcomes {
		if o.CheckID == string(id) {
			return o.Category
		}
	}
	if run.JudgmentOutcome != nil && run.JudgmentOutcome.CheckID == string(id) {
		return run.JudgmentOutcome.Category
	}
	return ""
}
