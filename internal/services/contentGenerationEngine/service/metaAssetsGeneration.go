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
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *contentGenerationEngineService) HandleMetaAssetsGeneration(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("meta assets generation: %w", err)
	}

	promptData := buildMetaAssetsPromptData(mc, kw)

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.MetaAssetsGeneration, promptData)
	if err != nil {
		return fmt.Errorf("meta assets generation: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.values.MetaAssetsMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("meta assets generation: haiku call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("meta assets generation: clean response: %w", err)
	}

	var meta models.CGEMetaAssets
	if err := json.Unmarshal([]byte(cleaned), &meta); err != nil {
		return fmt.Errorf("meta assets generation: unmarshal response: %w", err)
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		MetaAssets: &meta,
		ProcessMetadata: &models.CGEProcessMetadata{
			PostArticleStatus: models.CGEPostArticleStatus{
				MetaAssetsGenerationDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("meta assets generation: save: %w", err)
	}

	return s.checkMetaCompletionAndDispatchFinalAssembly(ctx, masterContextID, userId)
}

func (s *contentGenerationEngineService) checkMetaCompletionAndDispatchFinalAssembly(ctx context.Context, masterContextID, userID string) error {
	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("meta fan-in: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("meta fan-in: master context not found: %s", masterContextID)
	}

	if !mc.ProcessMetadata.PostArticleStatus.AllMetaDone() {
		return nil
	}

	won, err := s.store.AtomicClaimFinalAssemblyDispatch(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("meta fan-in: atomic claim: %w", err)
	}
	if !won {
		return nil
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEFinalAssembly), userID, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}

func buildMetaAssetsPromptData(mc *models.WebEntityMasterContext, kw *models.Keyword) cgeDto.MetaAssetsGenerationPrompt {
	p := cgeDto.MetaAssetsGenerationPrompt{
		Keyword:     kw.Keyword,
		ArticleType: mc.ArticleType,
		ToneProfile: mc.ToneProfile,
	}

	if mc.Outline != nil {
		var outline map[string]interface{}
		if err := json.Unmarshal([]byte(mc.Outline.RawJSON), &outline); err == nil {
			if h1, ok := outline["h1"].(string); ok {
				p.H1 = h1
			}
		}
	}

	if bc := mc.BusinessContext; bc != nil {
		p.BrandVoiceTone = commonutils.Deref(bc.BrandVoiceSignals)
	}

	// First paragraph from the article content (images already replaced at this point)
	p.FirstParagraph = extractFirstParagraph(mc.ArticleContent)

	lines := strings.Split(mc.ArticleContent, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "![") {
			continue
		}
		p.FirstParagraph = trimmed
		break
	}

	return p
}
