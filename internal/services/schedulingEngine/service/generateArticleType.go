package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	seDto "github.com/atharva-ng/crunch/internal/services/schedulingEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/prompts"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *schedulingEngineService) GenerateArticleType(ctx context.Context, userId string, payload se.SEArticleStepPayload) error {
	ok, sa, err := s.store.GetScheduledArticle(ctx, payload.ScheduledArticleID)
	if err != nil {
		return fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok {
		return fmt.Errorf("scheduled article not found: %s", payload.ScheduledArticleID)
	}

	we, kw, err := s.loadKeywordContext(ctx, sa)
	if err != nil {
		return err
	}

	promptDTO := seDto.ArticleTypePrompt{
		Keyword:      kw.Keyword,
		Intent:       kw.Intent,
		Funnel:       string(kw.Funnel),
		Volume:       kw.Volume,
		CPC:          kw.CPC,
		BusinessName: derefBusinessName(we),
		ProductType:  derefProductType(we),
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.ArticleType, promptDTO)
	if err != nil {
		return fmt.Errorf("construct article-type prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("article-type LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("clean LLM response: %w", err)
	}

	var parsed seDto.ArticleTypeResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return fmt.Errorf("unmarshal article-type response: %w", err)
	}

	articleType, valid := models.ParseArticleType(parsed.ArticleType)
	if !valid {
		return fmt.Errorf("unrecognised article type from LLM: %q", parsed.ArticleType)
	}

	if err := s.store.UpdateScheduledArticle(ctx, payload.ScheduledArticleID, models.ScheduledArticleUpdateReq{
		ArticleType: &articleType,
		Reasoning:   &parsed.Reasoning,
	}); err != nil {
		return fmt.Errorf("save article type: %w", err)
	}

	// 1:1 chain to title generation. Done here (rather than via DispatchNext)
	// so we can carry the per-article ScheduledArticleID — DispatchContext only
	// understands web-entity-level identity.
	titleMsg := se.SEArticleStepPayload{ScheduledArticleID: payload.ScheduledArticleID}
	if err := s.dispatcher.Dispatch(ctx, string(se.ProcessSchedulingGenerateTitle), userId, titleMsg); err != nil {
		return fmt.Errorf("dispatch title generation: %w", err)
	}

	return nil
}

func (s *schedulingEngineService) loadKeywordContext(ctx context.Context, sa *models.ScheduledArticle) (*models.WebEntity, *models.Keyword, error) {
	ok, we, err := s.store.GetWebEntityByID(ctx, sa.WebEntityID.Hex())
	if err != nil {
		return nil, nil, fmt.Errorf("get web entity: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("web entity not found: %s", sa.WebEntityID.Hex())
	}

	ok, kw, err := s.store.GetKeyword(ctx, sa.KeywordID.Hex())
	if err != nil {
		return nil, nil, fmt.Errorf("get keyword: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("source keyword not found for scheduled article %s", sa.ID.Hex())
	}
	return we, kw, nil
}

func derefBusinessName(we *models.WebEntity) string {
	if we == nil || we.BusinessContext == nil {
		return ""
	}
	return commonutils.Deref(we.BusinessContext.BusinessName)
}

func derefProductType(we *models.WebEntity) string {
	if we == nil || we.BusinessContext == nil {
		return ""
	}
	return commonutils.Deref(we.BusinessContext.ProductType)
}
