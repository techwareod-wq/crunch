package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/prompts"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type funnelClassificationResult struct {
	SequenceID int    `json:"sequence_id"`
	Funnel     string `json:"funnel"`
}

func (s *seoBlogGeneratorSiteIntelligence) ClassifyFunnelKeywords(ctx context.Context, userId string, payload sie.FunnelClassificationPayload) error {
	_, webEntityContext, err := models.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}

	// Entry guard (P2): if funnel classification (or a later stage) already
	// completed, drop this chunk *before* the LLM call. Funnel chunks carry no
	// per-chunk "done" marker, so without this stage-level skip a stale or
	// duplicate chunk from the inflated backlog would still hydrate keywords and
	// spend a model call before the completion gate discovers the stage is
	// finished — turning a backlog of stale chunks into real LLM cost.
	if webEntityContext.EffectiveStatus() >= models.SIEStatusFunnelClassificationDone {
		return nil
	}

	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, webEntityContext.WebEntityID.Hex())
	if err != nil {
		return fmt.Errorf("failed to find web entity: %w", err)
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", webEntityContext.WebEntityID.Hex())
	}

	keywords, err := models.GetKeywordsByIDs(ctx, payload.KeywordIDs)
	if err != nil {
		return fmt.Errorf("failed to hydrate keyword batch: %w", err)
	}
	if len(keywords) == 0 {
		return fmt.Errorf("no keywords resolved for funnel classification batch")
	}

	// Per-keyword guard: only classify keywords whose funnel is still null. The
	// stage-level entry guard above can't catch a redelivered or overlapping chunk
	// while the stage is still …Started, so an ID whose funnel was already set by an
	// earlier run would otherwise be re-sent to the LLM — wasted spend. Filter those
	// out in place. If the whole batch is already classified there is nothing to
	// prompt for, so fall straight through to the completion gate (which advances the
	// stage once every keyword is settled).
	pending := keywords[:0]
	for _, kw := range keywords {
		if kw.Funnel == "" {
			pending = append(pending, kw)
		}
	}
	keywords = pending
	if len(keywords) == 0 {
		return s.checkFunnelCompletionAndDispatchNext(ctx, userId, payload.WebEntityContextID)
	}

	bc := webEntity.BusinessContext
	if bc == nil {
		return fmt.Errorf("web entity %s has no business context", webEntity.ID.Hex())
	}
	promptDto := sieDto.FunnelClassificationPromptRequest{
		BusinessName:  commonutils.Deref(bc.BusinessName),
		ProductType:   commonutils.Deref(bc.ProductType),
		KeywordsBatch: keywords,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.FunnelClassification, promptDto)
	if err != nil {
		return fmt.Errorf("failed to construct funnel classification prompt: %w", err)
	}

	promptReq := dto.PromptRequest{
		Messages: []dto.Message{
			{Role: "user", Content: prompt},
		},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, promptReq)
	if err != nil {
		return fmt.Errorf("failed to prompt LLM for funnel classification: %w", err)
	}

	cleanedResponse, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return fmt.Errorf("failed to clean LLM response: %w", err)
	}

	var results []funnelClassificationResult
	if err := json.Unmarshal([]byte(cleanedResponse), &results); err != nil {
		return fmt.Errorf("failed to unmarshal funnel classification response: %w", err)
	}

	// Index input keywords by sequence_id so we can resolve LLM responses
	// back to the keyword document's ObjectID.
	inputBySeqID := make(map[int]models.Keyword, len(keywords))
	for _, kw := range keywords {
		inputBySeqID[kw.SequenceID] = kw
	}

	successFunnels := make(map[primitive.ObjectID]models.FunnelStage)
	respondedSeqIDs := make(map[int]struct{})

	for _, r := range results {
		kw, exists := inputBySeqID[r.SequenceID]
		if !exists {
			// LLM hallucinated a sequence_id that wasn't in the batch.
			// Capture it on the WEC for diagnosis and skip — the real
			// keyword (whose seq id the LLM presumably dropped) will fall
			// through to the failure/retry path below.
			payloadJSON, _ := json.Marshal(r)
			if appendErr := models.AppendErrorDiagnostic(ctx, payload.WebEntityContextID, models.SIEErrorData{
				Step:    "FunnelClassification",
				Message: string(payloadJSON),
			}); appendErr != nil {
				return fmt.Errorf("failed to record bad LLM response: %w", appendErr)
			}
			continue
		}
		respondedSeqIDs[r.SequenceID] = struct{}{}
		stage := models.ParseFunnelStage(r.Funnel)
		successFunnels[kw.ID] = stage
	}

	// Failed = inputs whose seq id was either omitted from the response or
	// returned with an unparseable funnel value.
	var failedKeywordIDs []primitive.ObjectID
	for seqID, kw := range inputBySeqID {
		if _, ok := successFunnels[kw.ID]; ok {
			continue
		}
		_ = respondedSeqIDs[seqID]
		failedKeywordIDs = append(failedKeywordIDs, kw.ID)
	}

	// UpdateKeywordFunnels is the authoritative, idempotent record of success
	// ($set funnel + clear funnel_failed). The completion gate counts from this
	// keyword state, so re-running a chunk re-applies the same $set as a no-op.
	if len(successFunnels) > 0 {
		if err := models.UpdateKeywordFunnels(ctx, successFunnels); err != nil {
			return fmt.Errorf("failed to update keyword funnels: %w", err)
		}
	}

	if len(failedKeywordIDs) > 0 {
		if payload.IsRetry {
			// Retry exhausted — mark these keywords permanently funnel-failed via an
			// idempotent per-keyword flag (replaces the non-idempotent $inc/$push).
			// They now count as "settled" without ever being classified.
			if err := models.MarkKeywordsFunnelFailed(ctx, failedKeywordIDs); err != nil {
				return fmt.Errorf("failed to mark keywords funnel-failed: %w", err)
			}
		} else {
			// First-pass misses: retry them once. They are deliberately left
			// unsettled (no funnel value, no failed flag) so the state-derived gate
			// below keeps waiting until the retry chunk classifies or fails them.
			retryPayload := sie.FunnelClassificationPayload{
				WebEntityContextID: payload.WebEntityContextID,
				KeywordIDs:         failedKeywordIDs,
				IsRetry:            true,
			}
			if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEFunnelClassification), userId, retryPayload); err != nil {
				return fmt.Errorf("failed to dispatch retry funnel classification: %w", err)
			}
		}
	}

	return s.checkFunnelCompletionAndDispatchNext(ctx, userId, payload.WebEntityContextID)
}

func (s *seoBlogGeneratorSiteIntelligence) checkFunnelCompletionAndDispatchNext(ctx context.Context, userId, webEntityContextID string) error {
	// Derive completion from authoritative keyword state (P5), never from the old
	// $inc counters that at-least-once redelivery could push past total: a keyword
	// is "settled" once it has a funnel value or the permanent-failure flag, both
	// written idempotently. Re-counting a keyword is a no-op, so the gate reflects
	// real progress and can't trip early (fixes RC1 + RC2).
	settled, total, err := models.CountFunnelSettledKeywords(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to count settled funnel keywords: %w", err)
	}
	if total == 0 || settled < total {
		return nil
	}

	// Atomic single advance (P3, fixes RC3): flip FunnelClassificationStarted →
	// FunnelClassificationDone exactly once. The error sentinel is in the from-set
	// because a sibling chunk's failure flips status to SIEStatusError (see
	// buildSIEHandler) while real progress stays at …Started in last_status — once
	// every keyword is settled the stage is genuinely complete, so the winner
	// clears the sentinel and hands off. Only the CAS winner dispatches the next
	// stage, so two chunks crossing the gate together can't both enqueue
	// OpportunityScore.
	claimed, err := models.TryAdvanceStatus(ctx, webEntityContextID,
		[]int{models.SIEStatusFunnelClassificationStarted, models.SIEStatusError},
		models.SIEStatusFunnelClassificationDone)
	if err != nil {
		return fmt.Errorf("failed to advance to funnel classification done: %w", err)
	}
	if !claimed {
		return nil
	}

	ok, wec, err := models.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil || !ok {
		return fmt.Errorf("failed to get web entity context for dispatch: %w", err)
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEFunnelClassification, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextID,
	})
}
