package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func (s *contentGenerationEngineService) HandleTavilySearch(ctx context.Context, userId string, payload cge.CGETavilySearchPayload) error {
	masterContextID := payload.WebEntityMasterContextID
	variant := payload.Variant
	query := payload.Query

	resp, err := s.tavily.Search(ctx, dto.TavilySearchRequest{
		Query:       query,
		SearchDepth: "basic",
		MaxResults:  3,
	})
	if err != nil {
		return fmt.Errorf("tavily search (%s): search call: %w", variant, err)
	}

	// Extract best result (highest score)
	var insight *models.CGETopicInsight
	if len(resp.Results) > 0 {
		best := resp.Results[0]
		for _, r := range resp.Results[1:] {
			if r.Score > best.Score {
				best = r
			}
		}
		insight = &models.CGETopicInsight{
			Insight:    best.Content,
			Source:     best.URL,
			SourceName: best.Title,
		}
	}

	// Save insight and mark done in a single concurrent-safe dot-notation update
	if err := s.store.SetTopicResearchInsightAndMarkDone(ctx, masterContextID, variant, insight); err != nil {
		return fmt.Errorf("tavily search (%s): save insight and mark done: %w", variant, err)
	}

	return s.checkResearchCompletionAndDispatchOutline(ctx, masterContextID, userId)
}
