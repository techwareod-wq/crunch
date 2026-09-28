package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
)

func (s *contentGenerationEngineService) HandleSerpGapAnalysis(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("serp gap analysis: %w", err)
	}

	promptData := cgeDto.SerpGapAnalysisPrompt{
		Keyword:      kw.Keyword,
		Intent:       kw.Intent,
		Structures:   buildStructureSummaries(mc.SerpData),
		PAAQuestions: mc.SerpData.PAAQuestions,
	}
	if mc.BusinessContext != nil && mc.BusinessContext.ICPSignals != nil && len(mc.BusinessContext.ICPSignals.Roles) > 0 {
		promptData.ICPRole = strings.Join(mc.BusinessContext.ICPSignals.Roles, ", ")
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.SerpGapAnalysis, promptData)
	if err != nil {
		return fmt.Errorf("serp gap analysis: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("serp gap analysis: LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("serp gap analysis: clean response: %w", err)
	}

	var gapAnalysis models.CGESerpGapAnalysis
	if err := json.Unmarshal([]byte(cleaned), &gapAnalysis); err != nil {
		return fmt.Errorf("serp gap analysis: parse response: %w", err)
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		SerpGapAnalysis: &gapAnalysis,
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				SerpGapAnalysisDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("serp gap analysis: save result: %w", err)
	}

	return s.checkResearchCompletionAndDispatchOutline(ctx, masterContextID, userId)
}
