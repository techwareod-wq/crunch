package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func (s *contentGenerationEngineService) HandleSerpFetch(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("serp fetch: %w", err)
	}

	// Call DataForSEO advanced SERP with inurl:blog filter
	resp, err := s.dataForSEO.GetAdvancedSerpResults(ctx, dto.GetAdvancedSerpRequest{
		Tasks: []dto.AdvancedSerpTask{{
			Keyword:      kw.Keyword,
			LocationCode: mc.LocationCode,
			LanguageCode: "en",
			Depth:        s.values.SerpFetchDepth,
			SearchParam:  "inurl:blog",
		}},
	})
	if err != nil {
		return fmt.Errorf("serp fetch: advanced SERP call: %w", err)
	}

	serpData := &models.CGESerpData{}
	var scrapeStatuses []models.UrlScrapeStatus

	if len(resp.Tasks) > 0 && len(resp.Tasks[0].Result) > 0 {
		result := resp.Tasks[0].Result[0]
		organicCount := 0
		for _, item := range result.Items {
			switch item.Type {
			case "organic":
				if organicCount < 3 {
					serpData.TopStructures = append(serpData.TopStructures, models.CGEArticleStructure{
						URL: item.URL,
					})
					scrapeStatuses = append(scrapeStatuses, models.UrlScrapeStatus{
						URL: item.URL,
					})
					organicCount++
				}
			case "people_also_ask":
				for _, paa := range item.PAAItems() {
					serpData.PAAQuestions = append(serpData.PAAQuestions, paa.Title)
				}
			case "featured_snippet":
				serpData.FeaturedSnippetPresent = true
				serpData.FeaturedSnippetURL = item.URL
			}
		}
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		SerpData: serpData,
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				SerpFetchDone: true,
			},
			UrlScrapeStatuses: scrapeStatuses,
		},
	}); err != nil {
		return fmt.Errorf("serp fetch: save SERP data: %w", err)
	}

	// DispatchNext triggers dispatchUrlScrapeFanOut
	return s.pipeline.DispatchNext(ctx, cge.ProcessCGESerpFetch, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID, // carrying master context ID
	})
}
