package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// HandleCollect is the ONE generic collect handler: it resolves the payload's
// collector, runs it, marks it done, and — as the fan-in CAS winner —
// dispatches the score stage. A collector error returns to SQS for retries;
// the registry wrapper routes the FINAL failure into HandleCollectFailure so
// a non-critical collector degrades to a constraint instead of wedging the
// fan-in (§8.3).
func (s *auditService) HandleCollect(ctx context.Context, userID string, p audit.AuditCollectPayload) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit collect: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit collect: run not found: %s", p.RunID)
	}
	if run.Status != models.AuditStatusCollecting {
		return nil // late redelivery (already scored) or errored run
	}

	collector, ok := collectors.ByID(s.collectors, core.CollectorID(p.CollectorID))
	if !ok {
		// Snapshotted before a rollback that removed the collector: record
		// the constraint and let the fan-in complete rather than wedge.
		log.Error("audit collect: unknown collector in snapshot", "collectorId", p.CollectorID, "runId", p.RunID)
		return s.finishCollector(ctx, userID, p, fmt.Sprintf("collector %s is not registered in this build", p.CollectorID))
	}

	if !collector.AppliesTo(run) {
		return s.finishCollector(ctx, userID, p, "")
	}

	if err := collector.Collect(ctx, s.deps(), run); err != nil {
		if collector.Critical() {
			return fmt.Errorf("audit collect: critical collector %s: %w", p.CollectorID, err)
		}
		return fmt.Errorf("audit collect: collector %s: %w", p.CollectorID, err)
	}

	return s.finishCollector(ctx, userID, p, "")
}

// HandleCollectFailure is called by the async registry wrapper when a
// collect message dies for good (final attempt or permanent error). The
// failed collector's absence becomes a constraint; dependent checks will
// skip and their categories renormalize or go scored:false.
func (s *auditService) HandleCollectFailure(ctx context.Context, p audit.AuditCollectPayload, msg string) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit collect failure: load run: %w", err)
	}
	if !found || run.Status != models.AuditStatusCollecting {
		return nil
	}
	userID := "lead"
	if run.UserID != nil {
		userID = run.UserID.Hex()
	}
	return s.finishCollector(ctx, userID, p,
		fmt.Sprintf("data source %s was unavailable: %s", p.CollectorID, msg))
}

// finishCollector marks the collector done (recording a constraint when it
// failed/degraded) and, when every expected collector has reported, wins the
// Collecting→Scoring CAS and dispatches AUDIT_SCORE — exactly one winner
// dispatches (SIE fan-in precedent).
func (s *auditService) finishCollector(ctx context.Context, userID string, p audit.AuditCollectPayload, constraintMsg string) error {
	if constraintMsg != "" {
		constraint := core.Finding{
			CheckID:        core.CheckID("collector." + p.CollectorID),
			Severity:       core.SeverityInfo,
			Title:          fmt.Sprintf("Data source %s unavailable for this audit", p.CollectorID),
			Detail:         constraintMsg + ". Checks depending on it were skipped and their points renormalized — the score reflects only what was measured.",
			Recommendation: "None — this is a transparency note; a later audit retries the source.",
			Falsifiability: "A later audit lists no constraint for this data source.",
		}
		if err := models.AppendAuditConstraint(ctx, p.RunID, constraint); err != nil {
			return fmt.Errorf("audit collect: record constraint: %w", err)
		}
	}

	updated, err := models.MarkAuditCollectorDone(ctx, p.RunID, p.CollectorID)
	if err != nil {
		return fmt.Errorf("audit collect: mark done: %w", err)
	}

	if !collectorSetComplete(updated.CollectorsExpected, updated.CollectorsDone) {
		return nil
	}
	won, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
		[]int{models.AuditStatusCollecting}, models.AuditStatusScoring, nil)
	if err != nil {
		return fmt.Errorf("audit collect: advance to scoring: %w", err)
	}
	if !won {
		return nil // another collector's completion already dispatched
	}
	return s.pipeline.DispatchNext(ctx, audit.ProcessAuditCollect, pipeline.DispatchContext{
		UserID:             userID,
		WebEntityContextID: p.RunID,
	})
}

func collectorSetComplete(expected, done []string) bool {
	doneSet := map[string]bool{}
	for _, id := range done {
		doneSet[id] = true
	}
	for _, id := range expected {
		if !doneSet[id] {
			return false
		}
	}
	return true
}
