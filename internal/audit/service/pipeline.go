package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

// buildPipeline assembles the audit DAG (built once at startup; a build
// error is a programming error and panics, the CGE precedent):
//
//	AUDIT_CRAWL ──(fan-out: one DispatchKeyed per snapshotted collector)──▶ AUDIT_COLLECT (×N)
//	AUDIT_COLLECT ──(fan-in: the CAS winner calls DispatchNext)──▶ AUDIT_SCORE
//	AUDIT_SCORE ────▶ AUDIT_JUDGE_CONTENT   [secondary/gated LLM queue]
//	AUDIT_JUDGE_CONTENT ─▶ AUDIT_SYNTHESIZE [secondary/gated LLM queue]
//	AUDIT_SYNTHESIZE ─▶ (terminal)
//
// DispatchContext.WebEntityContextID is repurposed to carry the RunID (the
// CGE repurposing precedent). The fan-out closure uses the service's
// interfaces.Dispatcher for DispatchKeyed so crawl-handler redeliveries
// collapse into one collect run per collector via the idempotency gate.
func (s *auditService) buildPipeline(d pipeline.Dispatcher) *pipeline.Pipeline {
	p, err := pipeline.NewBuilder(d).
		AddStage(pipeline.Stage{ProcessType: audit.ProcessAuditCrawl, Label: "Crawl"}).
		AddStage(pipeline.Stage{ProcessType: audit.ProcessAuditCollect, Label: "Collect"}).
		AddStage(pipeline.Stage{ProcessType: audit.ProcessAuditScore, Label: "Score"}).
		AddStage(pipeline.Stage{ProcessType: audit.ProcessAuditJudgeContent, Label: "Judge Content"}).
		AddStage(pipeline.Stage{ProcessType: audit.ProcessAuditSynthesize, Label: "Synthesize"}).
		AddEdge(audit.ProcessAuditCrawl, audit.ProcessAuditCollect, s.dispatchCollectFanOut).
		AddEdge(audit.ProcessAuditCollect, audit.ProcessAuditScore, dispatchAuditRun).
		AddEdge(audit.ProcessAuditScore, audit.ProcessAuditJudgeContent, dispatchAuditRun).
		AddEdge(audit.ProcessAuditJudgeContent, audit.ProcessAuditSynthesize, dispatchAuditRun).
		Build()
	if err != nil {
		panic(fmt.Errorf("failed to build audit pipeline: %w", err))
	}
	return p
}

// dispatchAuditRun forwards the RunID to a single-flight stage.
func dispatchAuditRun(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	return d.Dispatch(ctx, string(next), dc.UserID, audit.AuditRunPayload{RunID: dc.WebEntityContextID})
}

// dispatchCollectFanOut sends one keyed AUDIT_COLLECT message per collector
// SNAPSHOTTED on the run at fan-out time (never the live registry — registry
// growth must not wedge in-flight runs). One generic process type for every
// collector is the extensibility choice: a new collector changes zero
// pipeline code.
func (s *auditService) dispatchCollectFanOut(ctx context.Context, _ pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	runID := dc.WebEntityContextID
	found, run, err := models.FindAuditRunByID(ctx, runID)
	if err != nil {
		return fmt.Errorf("audit collect fan-out: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit collect fan-out: run not found: %s", runID)
	}
	for _, collectorID := range run.CollectorsExpected {
		key := collectDispatchKey(runID, collectorID, run.RetryCount)
		if err := s.dispatcher.DispatchKeyed(ctx, string(next), dc.UserID, key, audit.AuditCollectPayload{
			RunID:       runID,
			CollectorID: collectorID,
		}); err != nil {
			return fmt.Errorf("audit collect fan-out: dispatch %s: %w", collectorID, err)
		}
	}
	return nil
}

// collectDispatchKey is the collect fan-out's idempotency key. It carries the
// run's retry count so an admin resume dispatches FRESH keys — without the
// salt, the processed-messages ledger would collapse a resumed run's collect
// messages into the already-consumed first-attempt keys and wedge the fan-in.
func collectDispatchKey(runID, collectorID string, retryCount int) string {
	return fmt.Sprintf("audit:%s:collect:%s:r%d", runID, collectorID, retryCount)
}

// expectedCollectorIDs resolves the fan-out set for a run (snapshotted onto
// the run doc at crawl completion).
func (s *auditService) expectedCollectorIDs(run *models.AuditRun) []string {
	applicable := collectors.FanOut(s.collectors, run)
	out := make([]string, 0, len(applicable))
	for _, c := range applicable {
		out = append(out, string(c.ID()))
	}
	return out
}
