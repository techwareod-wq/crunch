package service

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// AdminResumeRun resumes a failed run from its failed stage. Error.Stage
// names the dead stage; every stage handler CAS-advances from a known entry
// status and is redelivery-idempotent, so resume is: clear the error, bump
// the retry bookkeeping, reset status to the entry status, re-dispatch.
// `permanent` does NOT block a resume — permanent means "don't auto-redeliver",
// and the whole point is re-running after the operator/user fixed the cause.
//
// Artifact-TTL guard: collect/score resumes need the crawl artifact (the
// critical prerequisite — the deep-pass sample and most checks hang off it);
// when it TTL-expired the resume degrades to a full crawl restart and says so
// — never a wedged half-resume.
func (s *auditService) AdminResumeRun(ctx context.Context, runID string, adminID primitive.ObjectID) (*audit.AdminResumeResult, error) {
	found, run, err := models.FindAuditRunByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("audit resume: load run: %w", err)
	}
	if !found {
		return nil, audit.ErrAuditReportNotFound
	}
	// Quality uplift 4.1: a COMPLETED run whose judgment soft-failed is
	// resumable at the judge stage — re-running judge + synthesize fills the
	// missing judgment in and re-embeds the report.
	if run.Status == models.AuditStatusComplete {
		if run.JudgmentOutcome != nil {
			return nil, audit.ErrAuditRunNotFailed
		}
		won, err := models.TryResumeCompletedAuditRun(ctx, run.ID.Hex(), models.AuditStatusJudging, adminID)
		if err != nil {
			return nil, fmt.Errorf("audit resume: %w", err)
		}
		if !won {
			return nil, audit.ErrAuditRunNotFailed
		}
		if err := s.dispatcher.Dispatch(ctx, string(audit.ProcessAuditJudgeContent), dispatchUserID(run),
			audit.AuditRunPayload{RunID: run.ID.Hex()}); err != nil {
			s.markResumeDispatchFailure(ctx, run.ID.Hex(), audit.ProcessAuditJudgeContent)
			return nil, fmt.Errorf("audit resume: dispatch judge: %w", err)
		}
		return &audit.AdminResumeResult{ResumedFrom: "judge", Note: "completed run missing its content judgment; re-runs judge + synthesize"}, nil
	}

	if run.Status != models.AuditStatusError {
		return nil, audit.ErrAuditRunNotFailed
	}

	stage := ""
	if run.Error != nil {
		stage = run.Error.Stage
	}

	switch stage {
	case string(audit.ProcessAuditCollect):
		if !s.hasArtifact(ctx, run, artifacts.KindCrawl) {
			return s.resumeFromCrawl(ctx, run, adminID, "artifacts expired")
		}
		return s.resumeCollect(ctx, run, adminID, "")

	case string(audit.ProcessAuditScore):
		if !s.hasArtifact(ctx, run, artifacts.KindCrawl) {
			return s.resumeFromCrawl(ctx, run, adminID, "artifacts expired")
		}
		return s.resumeSingleStage(ctx, run, adminID,
			models.AuditStatusScoring, audit.ProcessAuditScore, "score")

	case string(audit.ProcessAuditJudgeContent):
		// Outcomes live on the run doc (never TTL'd); a missing bundle
		// soft-fails the judgment — the normal degradation, not a blocker.
		return s.resumeSingleStage(ctx, run, adminID,
			models.AuditStatusJudging, audit.ProcessAuditJudgeContent, "judge")

	case string(audit.ProcessAuditSynthesize):
		return s.resumeSingleStage(ctx, run, adminID,
			models.AuditStatusSynthesizing, audit.ProcessAuditSynthesize, "synthesize")

	default:
		// AUDIT_CRAWL, or an unknown/missing stage. One special case: a
		// crawl-stage death AFTER the Crawling→Collecting CAS (the fan-out
		// dispatch failed) left a valid crawl artifact + snapshot — resume
		// at the collect fan-out instead of re-spending the crawl.
		if len(run.CollectorsExpected) > 0 && s.hasArtifact(ctx, run, artifacts.KindCrawl) {
			return s.resumeCollect(ctx, run, adminID, "crawl data reused")
		}
		return s.resumeFromCrawl(ctx, run, adminID, "")
	}
}

// resumeFromCrawl is the full restart: clear every stage output and re-enter
// the crawl. When the stored OnPage task already finished, its results are
// refetched instead of re-crawling (no page re-spend) — one summary probe
// decides.
func (s *auditService) resumeFromCrawl(ctx context.Context, run *models.AuditRun, adminID primitive.ObjectID, why string) (*audit.AdminResumeResult, error) {
	toStatus := models.AuditStatusCreated
	reuseTask := false
	if run.OnPageTaskID != "" {
		if resp, err := s.dfs.GetOnPageSummary(ctx, run.OnPageTaskID); err == nil &&
			len(resp.Tasks) > 0 && len(resp.Tasks[0].Result) > 0 &&
			resp.Tasks[0].Result[0].CrawlProgress == "finished" {
			reuseTask = true
			toStatus = models.AuditStatusCrawling
		}
	}

	unset := []string{"collectors_expected", "collectors_done", "check_outcomes", "constraints", "judgment_outcome"}
	if !reuseTask {
		unset = append(unset, "onpage_task_id")
	}
	won, err := models.TryResumeAuditRun(ctx, run.ID.Hex(), toStatus, adminID, nil, unset)
	if err != nil {
		return nil, fmt.Errorf("audit resume: %w", err)
	}
	if !won {
		return nil, audit.ErrAuditRunNotFailed
	}

	if err := s.dispatchCrawl(ctx, run, dispatchUserID(run)); err != nil {
		return nil, err
	}

	note := "restarts the crawl — re-spends OnPage crawl pages"
	if reuseTask {
		note = "reuses the finished crawl task — no page re-spend"
	}
	if why != "" {
		note = why + "; " + note
	}
	return &audit.AdminResumeResult{ResumedFrom: "crawl", Note: note}, nil
}

// resumeCollect re-enters the collect fan-out, re-dispatching ONLY the
// collectors that never reported done (done collectors are not re-run; the
// retry-salted keys beat the processed-messages dedupe ledger).
func (s *auditService) resumeCollect(ctx context.Context, run *models.AuditRun, adminID primitive.ObjectID, why string) (*audit.AdminResumeResult, error) {
	won, err := models.TryResumeAuditRun(ctx, run.ID.Hex(), models.AuditStatusCollecting, adminID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("audit resume: %w", err)
	}
	if !won {
		return nil, audit.ErrAuditRunNotFailed
	}

	// Reload for the post-inc retry count — the dispatch keys must match
	// what a crawl-handler redelivery would compute from the stored doc.
	found, fresh, err := models.FindAuditRunByID(ctx, run.ID.Hex())
	if err != nil || !found {
		return nil, fmt.Errorf("audit resume: reload run: %w", err)
	}

	done := map[string]bool{}
	for _, id := range fresh.CollectorsDone {
		done[id] = true
	}
	userID := dispatchUserID(fresh)
	dispatched := 0
	for _, collectorID := range fresh.CollectorsExpected {
		if done[collectorID] {
			continue
		}
		key := collectDispatchKey(fresh.ID.Hex(), collectorID, fresh.RetryCount)
		if err := s.dispatcher.DispatchKeyed(ctx, string(audit.ProcessAuditCollect), userID, key, audit.AuditCollectPayload{
			RunID:       fresh.ID.Hex(),
			CollectorID: collectorID,
		}); err != nil {
			s.markResumeDispatchFailure(ctx, fresh.ID.Hex(), audit.ProcessAuditCollect)
			return nil, fmt.Errorf("audit resume: dispatch collector %s: %w", collectorID, err)
		}
		dispatched++
	}
	if dispatched == 0 {
		// Every collector already reported — the failure hit between fan-in
		// and score. Advance straight into scoring.
		return s.resumeScoreAfterCompleteFanIn(ctx, fresh, userID)
	}

	note := fmt.Sprintf("re-runs %d pending collector(s); completed collectors are reused", dispatched)
	if why != "" {
		note = why + "; " + note
	}
	return &audit.AdminResumeResult{ResumedFrom: "collect", Note: note}, nil
}

// resumeScoreAfterCompleteFanIn handles the collect resume that finds nothing
// left to collect: CAS into scoring and dispatch the score stage directly.
func (s *auditService) resumeScoreAfterCompleteFanIn(ctx context.Context, run *models.AuditRun, userID string) (*audit.AdminResumeResult, error) {
	if _, err := models.TryAdvanceAuditStatus(ctx, run.ID.Hex(),
		[]int{models.AuditStatusCollecting}, models.AuditStatusScoring, nil); err != nil {
		return nil, fmt.Errorf("audit resume: advance to scoring: %w", err)
	}
	if err := s.dispatcher.Dispatch(ctx, string(audit.ProcessAuditScore), userID,
		audit.AuditRunPayload{RunID: run.ID.Hex()}); err != nil {
		s.markResumeDispatchFailure(ctx, run.ID.Hex(), audit.ProcessAuditScore)
		return nil, fmt.Errorf("audit resume: dispatch score: %w", err)
	}
	return &audit.AdminResumeResult{ResumedFrom: "score", Note: "all collectors had completed; resumed at scoring"}, nil
}

// resumeSingleStage covers the single-flight stages: reset to the stage's
// entry status and re-dispatch its process type.
func (s *auditService) resumeSingleStage(ctx context.Context, run *models.AuditRun, adminID primitive.ObjectID,
	entryStatus int, process pipeline.ProcessType, label string) (*audit.AdminResumeResult, error) {

	won, err := models.TryResumeAuditRun(ctx, run.ID.Hex(), entryStatus, adminID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("audit resume: %w", err)
	}
	if !won {
		return nil, audit.ErrAuditRunNotFailed
	}
	if err := s.dispatcher.Dispatch(ctx, string(process), dispatchUserID(run),
		audit.AuditRunPayload{RunID: run.ID.Hex()}); err != nil {
		s.markResumeDispatchFailure(ctx, run.ID.Hex(), process)
		return nil, fmt.Errorf("audit resume: dispatch %s: %w", label, err)
	}
	return &audit.AdminResumeResult{ResumedFrom: label}, nil
}

// markResumeDispatchFailure flips the run back to Error when the re-dispatch
// itself failed, so it never sits silently in a pending status.
func (s *auditService) markResumeDispatchFailure(ctx context.Context, runID string, process pipeline.ProcessType) {
	if err := models.SetAuditRunError(ctx, runID, string(process), "admin resume dispatch failed", "", false); err != nil {
		log.Error("audit resume: mark dispatch failure", "error", err, "runId", runID)
	}
}

// hasArtifact reports whether a run's artifact of the given kind survived the
// TTL.
func (s *auditService) hasArtifact(ctx context.Context, run *models.AuditRun, kind core.Kind) bool {
	raws, err := models.LoadAuditArtifacts(ctx, run.ID)
	if err != nil {
		log.Warn("audit resume: artifact load failed", "error", err, "runId", run.ID.Hex())
		return false
	}
	_, ok := raws[kind]
	return ok
}

// dispatchUserID resolves the queue envelope's user attribution for a run.
func dispatchUserID(run *models.AuditRun) string {
	if run.UserID != nil {
		return run.UserID.Hex()
	}
	return "lead"
}
