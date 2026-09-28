package service

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/checks"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/engine"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// HandleJudgeContent runs the registered LLM judgment check(s) — V1: exactly
// one, content.llm_judgment. The stage owns the Anthropic call; the check
// owns prompt construction and parsing (Template Method). A final LLM
// failure is SOFT: Content/E-E-A-T renormalizes over its mechanical checks
// plus an Info constraint — the report must never wedge on Anthropic
// (decision 17's posture, extended per the ratified LLD).
func (s *auditService) HandleJudgeContent(ctx context.Context, userID string, p audit.AuditRunPayload) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit judge: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit judge: run not found: %s", p.RunID)
	}
	if run.Status != models.AuditStatusJudging {
		return nil
	}

	bundle, err := s.loadBundle(ctx, run)
	if err != nil {
		return fmt.Errorf("audit judge: %w", err)
	}

	evidence := engine.EvidenceByCheck(run.CheckOutcomes)
	in := checks.Input{Bundle: bundle, Run: run, Values: s.values}

	for _, llmCheck := range s.registry.LLMChecks() {
		if !bundle.HasAll(llmCheck.Requires()) {
			if err := s.softFailJudgment(ctx, p.RunID, llmCheck.ID(), "its required data was not collected"); err != nil {
				return err
			}
			continue
		}
		outcome, err := s.runJudgment(ctx, llmCheck, in, evidence)
		if err != nil {
			// Provider-internal retries already ran; this failure is final
			// and soft.
			log.Error("audit judge: LLM judgment failed, scoring on mechanical checks only", "check", llmCheck.ID(), "runId", p.RunID, "error", err)
			if err := s.softFailJudgment(ctx, p.RunID, llmCheck.ID(), "the content-quality model call failed"); err != nil {
				return err
			}
			continue
		}
		// $set (not append): a redelivered judge run overwrites, never
		// duplicates.
		if err := models.UpdateAuditRun(ctx, p.RunID, bson.M{"judgment_outcome": outcome}); err != nil {
			return fmt.Errorf("audit judge: persist outcome: %w", err)
		}
		// A success clears any stale soft-fail constraint from an earlier
		// attempt (the judgment-missing admin rerun path) so the
		// re-synthesized report doesn't claim the judgment is unavailable.
		if err := models.PullAuditConstraints(ctx, p.RunID, string(llmCheck.ID())); err != nil {
			log.Warn("audit judge: stale constraint cleanup failed", "runId", p.RunID, "error", err)
		}
	}

	if _, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
		[]int{models.AuditStatusJudging}, models.AuditStatusSynthesizing, nil); err != nil {
		return fmt.Errorf("audit judge: advance to synthesizing: %w", err)
	}
	return s.pipeline.DispatchNext(ctx, audit.ProcessAuditJudgeContent, pipeline.DispatchContext{
		UserID:             userID,
		WebEntityContextID: p.RunID,
	})
}

func (s *auditService) runJudgment(ctx context.Context, llmCheck checks.LLMCheck, in checks.Input,
	evidence map[core.CheckID]map[string]any) (*models.AuditCheckOutcome, error) {

	if s.llm == nil || s.llm.Anthropic == nil {
		return nil, fmt.Errorf("anthropic provider not configured")
	}

	prompt, err := llmCheck.BuildPrompt(in, evidence)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	model := s.values.LLM.JudgmentModel
	if model == "" {
		model = dto.AnthropicSonnet5
	}
	outcome, err := s.judgmentAttempt(ctx, llmCheck, prompt, model)
	if err == nil {
		return outcome, nil
	}

	// Quality uplift 4.1: one retry on a DIFFERENT model before the soft
	// fail — the primary's provider-internal retries already ran, so this
	// failure is model/final; a distinct model id dodges model-specific
	// outages. Only then does the caller soft-fail.
	fallback := s.values.LLM.JudgmentFallbackModel
	if fallback == "" {
		fallback = dto.AnthropicHaiku45
	}
	if fallback == model {
		return nil, err
	}
	log.Warn("audit judge: primary judgment call failed, retrying on fallback model",
		"check", llmCheck.ID(), "primary", model, "fallback", fallback, "error", err)
	outcome, fbErr := s.judgmentAttempt(ctx, llmCheck, prompt, fallback)
	if fbErr != nil {
		return nil, fmt.Errorf("primary (%s): %v; fallback (%s): %w", model, err, fallback, fbErr)
	}
	return outcome, nil
}

// judgmentAttempt is one full prompt→parse pass on one model.
func (s *auditService) judgmentAttempt(ctx context.Context, llmCheck checks.LLMCheck, prompt, model string) (*models.AuditCheckOutcome, error) {
	maxTokens := s.values.LLM.JudgmentMaxTokens
	if maxTokens <= 0 {
		maxTokens = s.llm.DefaultMaxTokens
	}
	resp, err := s.llm.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: dto.RoleUser, Content: prompt}},
		Model:     model,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}
	if resp.StopReason == dto.StopReasonMaxTokens {
		return nil, fmt.Errorf("LLM response truncated at max tokens")
	}

	cleaned, err := s.llm.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("clean response: %w", err)
	}
	result, err := llmCheck.ParseResult(cleaned)
	if err != nil {
		return nil, err
	}
	outcome := engine.OutcomeFromResult(llmCheck.ID(), llmCheck.Category(), result)
	return &outcome, nil
}

// softFailJudgment records the §8.3 constraint for a judgment that couldn't
// run; the category scores on its mechanical checks alone.
func (s *auditService) softFailJudgment(ctx context.Context, runID string, id core.CheckID, why string) error {
	constraint := core.Finding{
		CheckID:        id,
		Severity:       core.SeverityInfo,
		Title:          "Content quality judgment unavailable for this audit",
		Detail:         fmt.Sprintf("The %s check was skipped because %s. The Content & E-E-A-T score reflects its mechanical checks only.", id, why),
		Recommendation: "None — a later audit retries the judgment.",
		Falsifiability: "A later audit's report includes the content-quality judgment.",
	}
	if err := models.AppendAuditConstraint(ctx, runID, constraint); err != nil {
		return fmt.Errorf("audit judge: record soft-fail constraint: %w", err)
	}
	return nil
}
