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

func (s *contentGenerationEngineService) HandleSchemaGeneration(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("schema generation: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("schema generation: master context not found: %s", masterContextID)
	}

	promptData := buildSchemaPromptData(mc)

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.SchemaGeneration, promptData)
	if err != nil {
		return fmt.Errorf("schema generation: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.values.SchemaMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("schema generation: haiku call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("schema generation: clean response: %w", err)
	}

	var schema models.CGESchemaMarkup
	if err := json.Unmarshal([]byte(cleaned), &schema); err != nil {
		return fmt.Errorf("schema generation: unmarshal response: %w", err)
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		SchemaMarkup: &schema,
		ProcessMetadata: &models.CGEProcessMetadata{
			PostArticleStatus: models.CGEPostArticleStatus{
				SchemaGenerationDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("schema generation: save: %w", err)
	}

	return s.checkMetaCompletionAndDispatchFinalAssembly(ctx, masterContextID, userId)
}

func buildSchemaPromptData(mc *models.WebEntityMasterContext) cgeDto.SchemaGenerationPrompt {
	p := cgeDto.SchemaGenerationPrompt{}

	if mc.Outline != nil {
		var outline map[string]interface{}
		if err := json.Unmarshal([]byte(mc.Outline.RawJSON), &outline); err == nil {
			if h1, ok := outline["h1"].(string); ok {
				p.H1 = h1
			}
			if desc, ok := outline["meta_description"].(string); ok {
				p.MetaDescription = desc
			}
			if slug, ok := outline["url_slug"].(string); ok {
				p.URLSlug = slug
			}
		}
	}

	if bc := mc.BusinessContext; bc != nil {
		p.BusinessName = commonutils.Deref(bc.BusinessName)
	}

	p.FAQContent = extractFAQContent(mc.ArticleContent)

	return p
}

// extractFAQContent extracts the FAQ section from article markdown.
// Looks for a heading containing "FAQ" and returns everything below it.
func extractFAQContent(content string) string {
	lines := strings.Split(content, "\n")
	faqStart := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && strings.Contains(strings.ToUpper(trimmed), "FAQ") {
			faqStart = i + 1
			break
		}
	}

	if faqStart == -1 {
		return ""
	}

	return strings.Join(lines[faqStart:], "\n")
}
