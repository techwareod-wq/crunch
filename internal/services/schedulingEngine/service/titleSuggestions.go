package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	seDto "github.com/atharva-ng/crunch/internal/services/schedulingEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/prompts"
)

func (s *schedulingEngineService) GenerateTitleSuggestions(ctx context.Context, userId, scheduledArticleID string) ([]string, error) {
	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return nil, se.ErrScheduledArticleNotFound
	}
	if !titleEditable(sa) {
		return nil, se.ErrScheduledArticleNotEditable
	}

	// Generate-once: reuse the persisted set when present so repeat opens don't
	// re-spend tokens.
	if len(sa.TitleSuggestions) > 0 {
		return sa.TitleSuggestions, nil
	}

	we, kw, err := s.loadKeywordContext(ctx, sa)
	if err != nil {
		return nil, err
	}

	promptDTO := seDto.TitleSuggestionsPrompt{
		Keyword:      kw.Keyword,
		ArticleType:  string(sa.ArticleType),
		ICPRole:      icpRoles(we),
		BusinessName: derefBusinessName(we),
		Funnel:       string(kw.Funnel),
		ToneProfile:  styleTone(we),
		TitlePattern: styleTitlePattern(we),
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.TitleSuggestions, promptDTO)
	if err != nil {
		return nil, fmt.Errorf("construct title suggestions prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("title suggestions LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("clean LLM response: %w", err)
	}

	var parsed seDto.TitleSuggestionsResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal title suggestions response: %w", err)
	}

	titles := make([]string, 0, len(parsed.Titles))
	for _, t := range parsed.Titles {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			titles = append(titles, trimmed)
		}
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("LLM returned no title suggestions for %s", scheduledArticleID)
	}

	if err := s.store.UpdateScheduledArticle(ctx, scheduledArticleID, models.ScheduledArticleUpdateReq{
		TitleSuggestions: &titles,
	}); err != nil {
		return nil, fmt.Errorf("persist title suggestions: %w", err)
	}

	return titles, nil
}

// PreviewRetitleForType generates a title + reasoning for a changed article
// type without persisting anything (see SchedulingService). The caller commits
// the result via the dashboard edit endpoint only when the user saves.
func (s *schedulingEngineService) PreviewRetitleForType(ctx context.Context, userId, scheduledArticleID, newArticleType string) (string, string, error) {
	parsedType, valid := models.ParseArticleType(newArticleType)
	if !valid {
		return "", "", se.ErrInvalidArticleType
	}

	ok, sa, err := s.store.GetScheduledArticle(ctx, scheduledArticleID)
	if err != nil {
		return "", "", fmt.Errorf("get scheduled article: %w", err)
	}
	if !ok || sa.UserID.Hex() != userId {
		return "", "", se.ErrScheduledArticleNotFound
	}
	if !typeEditable(sa) {
		return "", "", se.ErrScheduledArticleNotEditable
	}

	we, kw, err := s.loadKeywordContext(ctx, sa)
	if err != nil {
		return "", "", err
	}

	promptDTO := seDto.RetitlePrompt{
		Keyword:      kw.Keyword,
		CurrentTitle: sa.Title,
		ArticleType:  string(parsedType),
		ICPRole:      icpRoles(we),
		BusinessName: derefBusinessName(we),
		Funnel:       string(kw.Funnel),
		ToneProfile:  styleTone(we),
		TitlePattern: styleTitlePattern(we),
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.RetitleForType, promptDTO)
	if err != nil {
		return "", "", fmt.Errorf("construct retitle prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return "", "", fmt.Errorf("retitle LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return "", "", fmt.Errorf("clean LLM response: %w", err)
	}

	var parsed seDto.RetitleResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return "", "", fmt.Errorf("unmarshal retitle response: %w", err)
	}

	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		return "", "", fmt.Errorf("LLM returned empty retitle for %s", scheduledArticleID)
	}

	return title, strings.TrimSpace(parsed.Reasoning), nil
}

// titleEditable mirrors the dashboard's per-status title-edit rule: a title is
// editable on scheduled / draft / readyForReview rows regardless of date.
// Only generating / published / scheduling are locked (enforced by callers).
func titleEditable(sa *models.ScheduledArticle) bool {
	switch sa.Status {
	case models.ScheduledArticleStatusScheduled,
		models.ScheduledArticleStatusDraft,
		models.ScheduledArticleStatusReadyForReview:
		return true
	default:
		return false
	}
}

// typeEditable mirrors the dashboard rule that article type is only editable on
// scheduled (pre-generation) rows, regardless of past/future schedule date.
func typeEditable(sa *models.ScheduledArticle) bool {
	return sa.Status == models.ScheduledArticleStatusScheduled
}
