package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	seDto "github.com/atharva-ng/crunch/internal/services/schedulingEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/prompts"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func (s *schedulingEngineService) GenerateTitle(ctx context.Context, userId string, payload se.SEArticleStepPayload) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, payload.ScheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok {
		return fmt.Errorf("scheduled article not found: %s", payload.ScheduledArticleID)
	}
	if sa.ArticleType == "" {
		return fmt.Errorf("title generation requires article type to be set first: %s", payload.ScheduledArticleID)
	}

	we, kw, err := s.loadKeywordContext(ctx, sa)
	if err != nil {
		return err
	}

	promptDTO := seDto.TitleGenerationPrompt{
		Keyword:      kw.Keyword,
		ArticleType:  string(sa.ArticleType),
		ICPRole:      icpRoles(we),
		BusinessName: derefBusinessName(we),
		Funnel:       string(kw.Funnel),
		ToneProfile:  styleTone(we),
		TitlePattern: styleTitlePattern(we),
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.TitleGeneration, promptDTO)
	if err != nil {
		return fmt.Errorf("construct title prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("title LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("clean LLM response: %w", err)
	}

	var parsed seDto.TitleGenerationResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return fmt.Errorf("unmarshal title response: %w", err)
	}

	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		log.Error("title generation returned empty title",
			"scheduledArticleID", payload.ScheduledArticleID,
			"cleanedResponse", cleaned,
		)
		return fmt.Errorf("LLM returned empty title for %s", payload.ScheduledArticleID)
	}

	update := models.ScheduledArticleUpdateReq{Title: &title}
	// A single user-initiated slot is created in the transient "scheduling"
	// state; landing the title is its terminal enrichment step, so promote it
	// to "scheduled" here. Bulk-scheduled slots default to "scheduled" already,
	// so this branch is a no-op for them.
	if sa.Status == models.ScheduledArticleStatusScheduling {
		scheduled := models.ScheduledArticleStatusScheduled
		update.Status = &scheduled
	}
	if err := s.store.UpdateScheduledArticle(ctx, payload.ScheduledArticleID, update); err != nil {
		return fmt.Errorf("save title: %w", err)
	}

	return s.markSchedulingDoneIfComplete(ctx, sa.WebEntityContextID.Hex())
}

// markSchedulingDoneIfComplete checks whether every scheduled article in the
// cohort now has a title and, if so, promotes the WEC to
// SIEStatusSchedulingDone — the gate CGE Orchestrate waits on. The completion
// signal is derived from a live count of titled docs (rather than an $inc
// counter) so at-least-once re-delivery of GenerateTitle can't overshoot the
// target. Idempotent: concurrent late-arrivers all observe the same condition
// and write the same status value.
func (s *schedulingEngineService) markSchedulingDoneIfComplete(ctx context.Context, webEntityContextID string) error {
	ok, wec, err := s.store.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}
	if wec.Status >= models.SIEStatusSchedulingDone {
		return nil
	}

	// While the upgrade re-run is rewinding the pipeline over the trial data, a
	// stale redelivery of a trial-cohort GenerateTitle must not promote the WEC
	// past the stage the re-run is actually at — the stage claims are status-CAS
	// driven and a jump to SchedulingDone would strand the re-run mid-pipeline.
	// ExtendSchedule re-checks completion right after it finalizes the upgrade.
	if wec.UpgradeState == models.WECUpgradeStateExpanding {
		return nil
	}

	total := wec.ProcessMetadata.SchedulingMetadata.Total
	if total == 0 {
		return nil
	}

	titled, err := s.store.CountScheduledArticlesWithTitleForContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("count titled scheduled articles: %w", err)
	}
	if titled < int64(total) {
		return nil
	}

	// CAS the ClusteringDone → SchedulingDone transition so exactly one worker
	// wins it, even under concurrent late GenerateTitle re-deliveries. The
	// winner owns the one-shot kickoff below; losers observe claimed=false and
	// no-op. Single-shot matters here: the master-context collection has no
	// unique index (retries delete+recreate), so a duplicated CGE kickoff could
	// double-create a context and generate — and bill — the article twice.
	claimed, err := s.store.TryAdvanceWECStatus(ctx, webEntityContextID,
		[]int{models.SIEStatusClusteringDone}, models.SIEStatusSchedulingDone)
	if err != nil {
		return fmt.Errorf("mark scheduling done: %w", err)
	}
	if !claimed {
		return nil
	}

	// Auto-start the first calendar article so the user lands on a dashboard
	// with content already generating. Fires for both trial and full onboarding
	// (they converge on this path). Skipped on the trial→paid upgrade re-run,
	// which restores SchedulingDone through here too (upgrade_state == complete)
	// but whose articles already exist. Best-effort: the pipeline state is
	// committed and the CAS has closed, so a failed dispatch is logged rather
	// than returned — returning would only re-run the title LLM without being
	// able to re-dispatch (the guard/CAS short-circuit the retry).
	if wec.UpgradeState != models.WECUpgradeStateComplete {
		if err := s.dispatchFirstArticleGeneration(ctx, webEntityContextID); err != nil {
			log.Error("auto-dispatch of first article generation failed after scheduling done",
				"error", err, "webEntityContextId", webEntityContextID)
		}
	}
	return nil
}

// dispatchFirstArticleGeneration kicks off content generation for the earliest
// article on the just-completed calendar by dispatching a CGE_ORCHESTRATE event
// — the same entry point the dashboard "Generate" button uses. The CGE
// scheduling-done gate it lands on is already open because the caller advanced
// the WEC first. Payload fields are copied straight off the scheduled-article
// doc, the same mapping the manual ResolveOrchestrateContext path performs.
func (s *schedulingEngineService) dispatchFirstArticleGeneration(ctx context.Context, webEntityContextID string) error {
	ok, sa, err := s.store.GetFirstScheduledArticleForContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("load first scheduled article: %w", err)
	}
	if !ok {
		// No calendar rows to generate from. Total>0 was asserted above, so this
		// is defensive; treat as a benign no-op.
		return nil
	}

	payload := cge.CGEOrchestratePayload{
		ScheduledArticleID:     sa.ID.Hex(),
		WebEntityContextID:     sa.WebEntityContextID.Hex(),
		KeywordID:              sa.KeywordID.Hex(),
		ArticleType:            string(sa.ArticleType),
		ProposedTitle:          sa.Title,
		AdditionalInstructions: sa.AdditionalInstructions,
		InternalLinkingEnabled: sa.InternalLinkingEnabled,
		ThumbnailStyle:         sa.ThumbnailStyle,
	}
	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEOrchestrate), sa.UserID.Hex(), payload)
}

func icpRoles(we *models.WebEntity) string {
	if we == nil || we.BusinessContext == nil || we.BusinessContext.ICPSignals == nil {
		return ""
	}
	return strings.Join(we.BusinessContext.ICPSignals.Roles, ", ")
}

// styleTone is the tone profile as title-call fallback context, "" when the
// entity never ran a style learn OR the user toggled title styling off —
// disabling styled titles must not fall back to the body tone (nil-safe all
// the way down). Title templates only render it when no title pattern exists.
func styleTone(we *models.WebEntity) string {
	return we.StyleProfile().TitleToneFallback()
}

// styleTitlePattern is the learned TITLE pattern — the proper context for a
// title call, derived from the publisher's existing titles specifically.
func styleTitlePattern(we *models.WebEntity) string {
	return we.StyleProfile().TitleStyle()
}
