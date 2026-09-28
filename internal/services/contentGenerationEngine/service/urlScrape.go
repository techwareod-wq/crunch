package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/utils"
)

func (s *contentGenerationEngineService) HandleUrlScrape(ctx context.Context, userId string, payload cge.CGEUrlScrapePayload) error {
	masterContextID := payload.WebEntityMasterContextID
	targetURL := payload.URL

	structure, scrapeErr := utils.ExtractArticleStructure(targetURL)
	if scrapeErr != nil {
		if err := s.store.MarkUrlScrapeFailed(ctx, masterContextID, targetURL); err != nil {
			return fmt.Errorf("url scrape: mark failed: %w (original: %v)", err, scrapeErr)
		}
	} else {
		// Save extracted structure via positional update (concurrent-safe)
		if err := s.store.UpdateCGEArticleStructure(ctx, masterContextID, targetURL, models.CGEArticleStructure{
			URL:             targetURL,
			H1:              structure.H1,
			H2s:             structure.H2s,
			H3s:             structure.H3s,
			WordCount:       structure.WordCount,
			MetaDescription: structure.MetaDescription,
		}); err != nil {
			return fmt.Errorf("url scrape: save structure: %w", err)
		}

		if err := s.store.MarkUrlScrapeDone(ctx, masterContextID, targetURL); err != nil {
			return fmt.Errorf("url scrape: mark done: %w", err)
		}
	}

	// Atomically decrement the remaining counter — only the worker that hits 0 proceeds
	remaining, err := s.store.DecrementUrlScrapesRemaining(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("url scrape: decrement remaining: %w", err)
	}
	if remaining > 0 {
		return nil // more scrapes pending
	}

	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("url scrape: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("url scrape: master context not found: %s", masterContextID)
	}

	totalWordCount := 0
	successCount := 0
	if mc.SerpData != nil {
		for _, ts := range mc.SerpData.TopStructures {
			if ts.WordCount > 0 {
				totalWordCount += ts.WordCount
				successCount++
			}
		}
	}

	avgWordCount := 0
	targetWordCount := 0
	if successCount > 0 {
		avgWordCount = totalWordCount / successCount
		targetWordCount = int(float64(avgWordCount) * s.values.ArticleLengthMultiplier)
	}

	if mc.SerpData != nil {
		mc.SerpData.AverageWordCount = avgWordCount
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		SerpData:        mc.SerpData,
		TargetWordCount: &targetWordCount,
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				UrlScrapeDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("url scrape: finalize: %w", err)
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGESerpGapAnalysis), userId, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}
