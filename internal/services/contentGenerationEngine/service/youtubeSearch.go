package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func (s *contentGenerationEngineService) HandleYouTubeSearch(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	_, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("youtube search: %w", err)
	}

	resp, err := s.youtube.SearchVideos(ctx, dto.YouTubeSearchRequest{
		Query:      kw.Keyword,
		MaxResults: 5,
	})
	if err != nil {
		return fmt.Errorf("youtube search: search call: %w", err)
	}

	items := resp.Items
	if len(items) > 2 {
		items = items[:2]
	}

	// 0 results — skip entire YouTube flow
	if len(items) == 0 {
		if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
			ProcessMetadata: &models.CGEProcessMetadata{
				ResearchStepStatus: models.CGEResearchStepStatus{
					YouTubeSearchDone:     true,
					YouTubeTranscriptDone: true,
					YouTubeSummaryDone:    true,
				},
			},
		}); err != nil {
			return fmt.Errorf("youtube search: mark skipped: %w", err)
		}
		return s.checkResearchCompletionAndDispatchOutline(ctx, masterContextID, userId)
	}

	videoStatuses := make([]models.YouTubeVideoStatus, len(items))
	for i, item := range items {
		videoStatuses[i] = models.YouTubeVideoStatus{
			VideoID: item.VideoID,
			Title:   item.Title,
			Channel: item.Channel,
		}
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				YouTubeSearchDone: true,
			},
		},
		YouTubeVideos: videoStatuses,
	}); err != nil {
		return fmt.Errorf("youtube search: save videos: %w", err)
	}

	// DispatchNext triggers dispatchYouTubeTranscriptFanOut
	return s.pipeline.DispatchNext(ctx, cge.ProcessCGEYouTubeSearch, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID, // carrying master context ID
	})
}
