package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func (s *contentGenerationEngineService) HandleFinalAssembly(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("final assembly: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("final assembly: master context not found: %s", masterContextID)
	}

	statusReady := models.CGEStatusReadyForReview

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		Status: &statusReady,
	}); err != nil {
		return fmt.Errorf("final assembly: save: %w", err)
	}

	// Mirror the readiness onto the calendar slot so the schedule UI can
	// surface the "ready for review" CTA without joining to the master
	// context. Best-effort when the slot is missing (manual trigger path).
	if !mc.ScheduledArticleID.IsZero() {
		if err := s.store.SetScheduledArticleStatus(ctx, mc.ScheduledArticleID.Hex(), models.ScheduledArticleStatusReadyForReview); err != nil {
			return fmt.Errorf("final assembly: mark scheduled article ready: %w", err)
		}
	}

	return nil
}
