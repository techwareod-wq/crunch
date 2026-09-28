package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/prompts"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/utils"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *seoBlogGeneratorSiteIntelligence) GetPreExpandedUserKeywords(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, metadata.WebEntityID)
	if err != nil {
		return err
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", metadata.WebEntityID)
	}

	preExpandedKeywords, err := s.generatePreExpandedSeeds(ctx, webEntity)
	if err != nil {
		return err
	}

	if err := models.UpdateWebEntityContext(ctx, metadata.WebEntityContextID, models.WECUpdateReq{
		Keywords:        &models.KeyWords{PreExpandedKeyWords: preExpandedKeywords},
		ProcessMetadata: &models.ProcessMetadata{DataFetchStepStatus: &models.SIEDataFetchStepStatus{PreExpandedKeywordsDone: true}},
	}); err != nil {
		return fmt.Errorf("failed to save pre-expanded keywords: %w", err)
	}

	// Best-effort status nudge: a lost CAS or transient write failure here is
	// non-blocking — the data-fetch fan-in re-derives progress from
	// EffectiveStatus — so the error is intentionally discarded.
	_ = models.SetStatusIfCurrent(ctx, metadata.WebEntityContextID, models.SIEStatusCreated, models.SIEStatusProcessing)

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEGetPreExpandedUserKeywords, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        metadata.WebEntityID,
		WebEntityContextID: metadata.WebEntityContextID,
	})
}

// generatePreExpandedSeeds runs the keyword-expansion prompt over the web
// entity's business context and returns the seed keyword list. Shared by the
// pre-expansion pipeline stage and the upgrade expand pipeline (which
// re-seeds only when the stored seeds are empty).
func (s *seoBlogGeneratorSiteIntelligence) generatePreExpandedSeeds(ctx context.Context, webEntity *models.WebEntity) ([]string, error) {
	bc := webEntity.BusinessContext
	if bc == nil {
		return nil, fmt.Errorf("web entity %s has no business context", webEntity.ID.Hex())
	}
	icp := bc.ICPSignals
	if icp == nil {
		icp = &models.ICPSignals{}
	}
	getExpandedKeywordDataPromptDto := sieDto.ExpandKeywordsPromptRequest{
		BusinessName:      commonutils.Deref(bc.BusinessName),
		ProductType:       commonutils.Deref(bc.ProductType),
		PrimaryUseCase:    commonutils.Deref(bc.PrimaryUseCase),
		KeyFeatures:       strings.Join(bc.KeyFeatures, ", "),
		KeyDifferentiator: commonutils.Deref(bc.KeyDifferentiator),
		Integrations:      strings.Join(bc.Integrations, ", "),
		IcpRoles:          strings.Join(icp.Roles, ", "),
		IcpPains:          strings.Join(icp.PainPoints, ", "),
		Competitors:       formatCompetitorNames(webEntity.Competitors),
	}
	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.UserKeywordExpansion, getExpandedKeywordDataPromptDto)
	if err != nil {
		return nil, fmt.Errorf("failed to construct prompt for expanding keywords: %w", err)
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
		return nil, fmt.Errorf("failed to prompt LLM: %w", err)
	}

	cleanedResponse, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to clean LLM response: %w", err)
	}

	preExpandedKeywords, err := utils.ParseExpandedKeywordsLLMResponse(cleanedResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expanded keywords: %w", err)
	}
	return preExpandedKeywords, nil
}

func (s *seoBlogGeneratorSiteIntelligence) GetExpandedUserKeywords(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	isWebEntityContext, webEntityContext, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}

	if !isWebEntityContext {
		return fmt.Errorf("web entity context not found: %s", metadata.WebEntityContextID)
	}

	preExpandedKeywords := webEntityContext.Keywords.PreExpandedKeyWords
	if len(preExpandedKeywords) == 0 {
		return fmt.Errorf("no pre-expanded keywords found for web entity context: %s", metadata.WebEntityContextID)
	}

	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, metadata.WebEntityID)
	if err != nil {
		return err
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", metadata.WebEntityID)
	}

	expandedKeywords, err := s.requestExpandedKeywordsData(ctx, preExpandedKeywords, s.limitsFor(webEntityContext).ExpandedKeywordsLimit, webEntity.LocationCode, s.resolveDomainRating(webEntity), webEntity.SEOStrategyOrDefault())
	if err != nil {
		return fmt.Errorf("failed to get expanded keyword data: %w", err)
	}

	if err := models.UpdateWebEntityContext(ctx, metadata.WebEntityContextID, models.WECUpdateReq{
		Keywords:        &models.KeyWords{ExpandedKeyWords: expandedKeywords},
		ProcessMetadata: &models.ProcessMetadata{DataFetchStepStatus: &models.SIEDataFetchStepStatus{ExpandedKeywordsDone: true}},
	}); err != nil {
		return fmt.Errorf("failed to save expanded keywords: %w", err)
	}

	// Best-effort status nudge: a lost CAS or transient write failure here is
	// non-blocking — the data-fetch fan-in re-derives progress from
	// EffectiveStatus — so the error is intentionally discarded.
	_ = models.SetStatusIfCurrent(ctx, metadata.WebEntityContextID, models.SIEStatusCreated, models.SIEStatusProcessing)

	return s.checkCompletionAndDispatchPostProcessing(ctx, sie.ProcessSIEGetExpandedUserKeywords, userId, metadata.WebEntityID, metadata.WebEntityContextID)
}
