package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
)

// checkResearchCompletionAndDispatchOutline is the shared fan-in gate.
// Every terminal research handler (SERP gap analysis, each Tavily search, YouTube summary)
// calls this after marking its step done.
func (s *contentGenerationEngineService) checkResearchCompletionAndDispatchOutline(ctx context.Context, masterContextID, userID string) error {
	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("fan-in: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("fan-in: master context not found: %s", masterContextID)
	}

	if !mc.ProcessMetadata.ResearchStepStatus.AllResearchDone() {
		return nil
	}

	// Atomic claim — only one caller wins the race
	won, err := s.store.AtomicClaimOutlineDispatch(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("fan-in: atomic claim: %w", err)
	}
	if !won {
		return nil
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEOutlineGeneration), userID, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}

// checkFanInFromDispatchFunc is a standalone fan-in check usable from pipeline DispatchFunc
// closures that don't have access to the service struct.
func checkFanInFromDispatchFunc(ctx context.Context, d pipeline.Dispatcher, masterContextID, userID string) error {
	_, mc, err := models.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("fan-in dispatch func: get master context: %w", err)
	}

	if !mc.ProcessMetadata.ResearchStepStatus.AllResearchDone() {
		return nil
	}

	won, err := models.AtomicClaimOutlineDispatch(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("fan-in dispatch func: atomic claim: %w", err)
	}
	if !won {
		return nil
	}

	return d.Dispatch(ctx, string(cge.ProcessCGEOutlineGeneration), userID, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}

// loadMasterContextAndKeyword fetches the WebEntityMasterContext along with
// the keyword doc it references. Every CGE handler that needs the keyword
// string / volume / funnel / etc. uses this — denormalised copies no longer
// live on the master context.
func (s *contentGenerationEngineService) loadMasterContextAndKeyword(ctx context.Context, masterContextID string) (*models.WebEntityMasterContext, *models.Keyword, error) {
	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return nil, nil, fmt.Errorf("get master context: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("master context not found: %s", masterContextID)
	}
	ok, kw, err := s.store.GetKeyword(ctx, mc.KeywordID.Hex())
	if err != nil {
		return nil, nil, fmt.Errorf("get keyword: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("keyword not found for master context %s: %s", masterContextID, mc.KeywordID.Hex())
	}
	return mc, kw, nil
}

// buildStructureSummaries extracts URL + H2s from TopStructures for the prompt DTO.
func buildStructureSummaries(serpData *models.CGESerpData) []cgeDto.StructureSummary {
	if serpData == nil {
		return nil
	}
	summaries := make([]cgeDto.StructureSummary, len(serpData.TopStructures))
	for i, ts := range serpData.TopStructures {
		summaries[i] = cgeDto.StructureSummary{
			URL: ts.URL,
			H2s: ts.H2s,
		}
	}
	return summaries
}
