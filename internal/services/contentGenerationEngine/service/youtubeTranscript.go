package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func (s *contentGenerationEngineService) HandleYouTubeTranscript(ctx context.Context, userId string, payload cge.CGEYouTubeTranscriptPayload) error {
	masterContextID := payload.WebEntityMasterContextID
	videoID := payload.VideoID

	transcript, err := s.youtube.GetTranscript(ctx, videoID)
	if err != nil || transcript == "" {
		if err != nil {
			log.Error("youtube transcript: fetch failed, skipping", "videoID", videoID, "error", err)
		} else {
			log.Warn("youtube transcript: empty transcript, skipping", "videoID", videoID)
		}
		if skipErr := s.store.MarkYouTubeTranscriptSkipped(ctx, masterContextID, videoID); skipErr != nil {
			return fmt.Errorf("youtube transcript: mark skipped: %w (original: %v)", skipErr, err)
		}
	} else {
		if len(transcript) > s.values.MaxTranscriptChars {
			transcript = transcript[:s.values.MaxTranscriptChars]
		}

		// Store transcript and mark video as done in a single update
		if err := s.store.SaveYouTubeTranscriptAndMarkDone(ctx, masterContextID, videoID, transcript); err != nil {
			return fmt.Errorf("youtube transcript: save transcript: %w", err)
		}
	}

	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("youtube transcript: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("youtube transcript: master context not found: %s", masterContextID)
	}

	hasTranscript := false
	for _, v := range mc.YouTubeVideos {
		if !v.Done && !v.Skipped && !v.Failed {
			return nil
		}
		if v.Done && v.Transcript != "" {
			hasTranscript = true
		}
	}

	if !hasTranscript {
		if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
			ProcessMetadata: &models.CGEProcessMetadata{
				ResearchStepStatus: models.CGEResearchStepStatus{
					YouTubeTranscriptDone: true,
					YouTubeSummaryDone:    true,
				},
			},
		}); err != nil {
			return fmt.Errorf("youtube transcript: mark all skipped: %w", err)
		}
		return s.checkResearchCompletionAndDispatchOutline(ctx, masterContextID, userId)
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		ProcessMetadata: &models.CGEProcessMetadata{
			ResearchStepStatus: models.CGEResearchStepStatus{
				YouTubeTranscriptDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("youtube transcript: mark done: %w", err)
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEYouTubeSummary), userId, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}
