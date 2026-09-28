package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *seoBlogGeneratorSiteIntelligence) GetCompetitorKeywords(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, metadata.WebEntityID)
	if err != nil {
		return err
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", metadata.WebEntityID)
	}

	limits, err := s.limitsForWECID(ctx, metadata.WebEntityContextID)
	if err != nil {
		return err
	}

	cleanedURL := commonutils.CleanURL(metadata.CompetitorURL)
	keywords, err := s.requestKeywordData(ctx, cleanedURL, limits.CompetitorKeywordsLimit, webEntity.LocationCode, s.resolveDomainRating(webEntity), webEntity.SEOStrategyOrDefault())
	if err != nil {
		_ = models.AppendErrorDiagnostic(ctx, metadata.WebEntityContextID, models.SIEErrorData{
			Step:    "competitor_keywords",
			Message: fmt.Sprintf("failed to get keyword data for competitor URL %s: %s", metadata.CompetitorURL, err.Error()),
		})
		keywords = []models.Keyword{}
	}

	for i := range keywords {
		keywords[i].CompetitorUrl = cleanedURL
	}

	if err := models.AppendCompetitorKeywords(ctx, metadata.WebEntityContextID, keywords); err != nil {
		return fmt.Errorf("failed to append competitor keywords for %s: %w", metadata.CompetitorURL, err)
	}

	ok, wec, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", metadata.WebEntityContextID)
	}

	if wec.CompetitorKeywordsProcessed >= wec.TotalCompetitors {
		if err := models.UpdateWebEntityContext(ctx, metadata.WebEntityContextID, models.WECUpdateReq{
			ProcessMetadata: &models.ProcessMetadata{DataFetchStepStatus: &models.SIEDataFetchStepStatus{CompetitorKeywordsDone: true}},
		}); err != nil {
			return fmt.Errorf("failed to mark competitor keywords done: %w", err)
		}
	}

	// Best-effort status nudge: a lost CAS or transient write failure here is
	// non-blocking — the data-fetch fan-in re-derives progress from
	// EffectiveStatus — so the error is intentionally discarded.
	_ = models.SetStatusIfCurrent(ctx, metadata.WebEntityContextID, models.SIEStatusCreated, models.SIEStatusProcessing)

	return s.checkCompletionAndDispatchPostProcessing(ctx, sie.ProcessSIEGetCompetitorKeywords, userId, metadata.WebEntityID, metadata.WebEntityContextID)
}
