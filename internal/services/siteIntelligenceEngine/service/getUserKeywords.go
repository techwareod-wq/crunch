package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *seoBlogGeneratorSiteIntelligence) GetUserKeywords(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
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

	// A failed user-keyword fetch is a soft failure: the run draws on competitor,
	// pre-expanded, and expanded keyword pools too, so we don't flip the WEC to
	// SIEStatusError or retry. Log it, record a non-blocking diagnostic, and
	// proceed with no user keywords so the data-fetch stage can still complete.
	userUrl := commonutils.CleanURL(webEntity.WebsiteUrl)
	keywords, err := s.requestKeywordData(ctx, userUrl, limits.UserKeywordsLimit, webEntity.LocationCode, s.resolveDomainRating(webEntity), webEntity.SEOStrategyOrDefault())
	if err != nil {
		log.Error("SIE: user keyword fetch failed, continuing without user keywords",
			"error", err, "webEntityContextId", metadata.WebEntityContextID, "userUrl", userUrl)
		if diagErr := models.AppendErrorDiagnostic(ctx, metadata.WebEntityContextID, models.SIEErrorData{
			Step:    string(sie.ProcessSIEGetUserKeywords),
			Message: fmt.Sprintf("failed to get keyword data for user URL %s: %v", userUrl, err),
		}); diagErr != nil {
			log.Error("SIE: failed to append user keyword diagnostic", "error", diagErr, "webEntityContextId", metadata.WebEntityContextID)
		}
		keywords = nil
	}

	if err := models.UpdateWebEntityContext(ctx, metadata.WebEntityContextID, models.WECUpdateReq{
		Keywords:        &models.KeyWords{UserKeyWords: keywords},
		ProcessMetadata: &models.ProcessMetadata{DataFetchStepStatus: &models.SIEDataFetchStepStatus{UserKeywordsDone: true}},
	}); err != nil {
		return fmt.Errorf("failed to save user keywords: %w", err)
	}

	// Best-effort status nudge: a lost CAS or transient write failure here is
	// non-blocking — the data-fetch fan-in re-derives progress from
	// EffectiveStatus — so the error is intentionally discarded.
	_ = models.SetStatusIfCurrent(ctx, metadata.WebEntityContextID, models.SIEStatusCreated, models.SIEStatusProcessing)

	return s.checkCompletionAndDispatchPostProcessing(ctx, sie.ProcessSIEGetUserKeywords, userId, metadata.WebEntityID, metadata.WebEntityContextID)
}
