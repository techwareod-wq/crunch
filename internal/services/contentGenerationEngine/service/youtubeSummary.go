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

func (s *contentGenerationEngineService) HandleYouTubeSummary(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("youtube summary: %w", err)
	}

	var sb strings.Builder
	for _, v := range mc.YouTubeVideos {
		if v.Transcript == "" {
			continue
		}
		fmt.Fprintf(&sb, "=== Video: \"%s\" by %s ===\n%s\n\n", v.Title, v.Channel, v.Transcript)
	}
	combined := sb.String()
	if len(combined) > s.values.MaxCombinedTranscriptChars {
		combined = combined[:s.values.MaxCombinedTranscriptChars]
	}

	promptData := cgeDto.YouTubeSummaryPrompt{
		Keyword:             kw.Keyword,
		CombinedTranscripts: combined,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.YouTubeSummary, promptData)
	if err != nil {
		return fmt.Errorf("youtube summary: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("youtube summary: LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("youtube summary: clean response: %w", err)
	}

	var insights models.CGEYouTubeInsights
	if err := json.Unmarshal([]byte(cleaned), &insights); err != nil {
		return fmt.Errorf("youtube summary: parse response: %w", err)
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		YouTubeInsights: &insights,
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				YouTubeSummaryDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("youtube summary: save result: %w", err)
	}

	return s.checkResearchCompletionAndDispatchOutline(ctx, masterContextID, userId)
}
