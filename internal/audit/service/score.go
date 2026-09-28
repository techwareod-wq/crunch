package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/engine"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

// HandleScore loads the blackboard, runs every deterministic check through
// the engine, persists the outcomes (evidence included — the judge stage
// reads it), and advances to judging.
func (s *auditService) HandleScore(ctx context.Context, userID string, p audit.AuditRunPayload) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit score: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit score: run not found: %s", p.RunID)
	}
	if run.Status != models.AuditStatusScoring {
		return nil // redelivery after advance, or errored run
	}

	bundle, err := s.loadBundle(ctx, run)
	if err != nil {
		return fmt.Errorf("audit score: %w", err)
	}

	outcomes, skipConstraints := engine.RunDeterministic(ctx, bundle, s.registry, run, s.values)

	// Full-list $set keeps a redelivered score run byte-identical.
	allConstraints := append(append([]core.Finding{}, run.Constraints...), skipConstraints...)
	if err := models.SetAuditCheckOutcomes(ctx, p.RunID, outcomes, allConstraints); err != nil {
		return fmt.Errorf("audit score: persist outcomes: %w", err)
	}

	if _, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
		[]int{models.AuditStatusScoring}, models.AuditStatusJudging, nil); err != nil {
		return fmt.Errorf("audit score: advance to judging: %w", err)
	}
	return s.pipeline.DispatchNext(ctx, audit.ProcessAuditScore, pipeline.DispatchContext{
		UserID:             userID,
		WebEntityContextID: p.RunID,
	})
}

func (s *auditService) loadBundle(ctx context.Context, run *models.AuditRun) (*artifacts.Bundle, error) {
	raw, err := models.LoadAuditArtifacts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return artifacts.BuildBundle(raw)
}
