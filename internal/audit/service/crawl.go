package service

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/targetcheck"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// HandleCrawl owns the OnPage task lifecycle: task_post, the in-handler
// summary poll (scope V1 build §1 — the AUDIT_CRAWL visibility override
// covers the long hold), result normalization via the crawl collector, the
// CollectorsExpected snapshot, and the collect fan-out. Redelivery-safe at
// every step: a stored task id is re-polled (never re-posted), and a run
// already in Collecting just re-fires the keyed fan-out.
func (s *auditService) HandleCrawl(ctx context.Context, userID string, p audit.AuditRunPayload) error {
	found, run, err := models.FindAuditRunByID(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("audit crawl: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("audit crawl: run not found: %s", p.RunID)
	}
	dc := pipeline.DispatchContext{UserID: userID, WebEntityContextID: p.RunID}

	switch {
	case run.Status == models.AuditStatusError, run.Status > models.AuditStatusCollecting:
		return nil // terminal or already past this stage — redelivery no-op
	case run.Status == models.AuditStatusCollecting:
		// Crashed between the CAS and the fan-out: re-fire the keyed fan-out
		// (the idempotency gate collapses duplicates).
		return s.pipeline.DispatchNext(ctx, audit.ProcessAuditCrawl, dc)
	}

	// Lead targets re-validate at crawl time (DNS-rebinding posture:
	// validate close to use — the deep pass will fetch this host directly).
	if core.RunKind(run.Kind) == core.RunKindLead {
		if _, _, err := targetcheck.Validate(ctx, run.TargetURL); err != nil {
			return fmt.Errorf("audit crawl: target failed re-validation (%v): %w", err, pipeline.ErrPermanent)
		}
	}

	if run.Status == models.AuditStatusCreated {
		if _, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
			[]int{models.AuditStatusCreated}, models.AuditStatusCrawling, nil); err != nil {
			return fmt.Errorf("audit crawl: mark crawling: %w", err)
		}
	}

	// Post the OnPage task once; a redelivery re-polls the stored id.
	taskID := run.OnPageTaskID
	if taskID == "" {
		taskID, err = s.postOnPageTask(ctx, run)
		if err != nil {
			return err
		}
		if err := models.UpdateAuditRun(ctx, p.RunID, bson.M{"onpage_task_id": taskID}); err != nil {
			return fmt.Errorf("audit crawl: store task id: %w", err)
		}
		run.OnPageTaskID = taskID
	}

	summary, err := s.pollOnPageSummary(ctx, taskID)
	if err != nil {
		return err
	}

	// Unreachable / robots-blocked / all-4xx targets are typed, permanent,
	// user-visible failures — never a guessed report (§8.3 row 1).
	if summary.CrawlStatus == nil || summary.CrawlStatus.PagesCrawled == 0 {
		return fmt.Errorf("audit crawl: target yielded zero crawlable pages [%s]: %w",
			audit.AuditReasonTargetUnreachable, pipeline.ErrPermanent)
	}

	// Normalize + persist the crawl artifact (includes deep-pass sampling).
	crawlCollector, ok := collectors.ByID(s.collectors, collectors.CollectorCrawl)
	if !ok {
		return fmt.Errorf("audit crawl: crawl collector not registered: %w", pipeline.ErrPermanent)
	}
	if err := crawlCollector.Collect(ctx, s.deps(), run); err != nil {
		return fmt.Errorf("audit crawl: normalize: %w", err)
	}

	// Resolve the run's ranking market BEFORE the fan-out so the authority +
	// serp collectors query the right country (Labs data is per-location —
	// the US default reads "0 ranked keywords" for any non-US market).
	// Best-effort: a stamp failure logs and the collectors fall back to US.
	if run.LocationCode == 0 {
		if loc := s.resolveRunLocation(ctx, run); loc != 0 {
			name := s.locations.NameForCode(loc)
			if err := models.UpdateAuditRun(ctx, p.RunID, bson.M{"location_code": loc, "location_name": name}); err != nil {
				log.Warn("audit crawl: location stamp failed, collectors default to US", "runId", p.RunID, "error", err)
			} else {
				run.LocationCode = loc
				run.LocationName = name
			}
		}
	}

	// Snapshot the fan-out set, advance, fan out. The CAS keeps a redelivery
	// from re-snapshotting after collectors already reported done.
	expected := s.expectedCollectorIDs(run)
	if _, err := models.TryAdvanceAuditStatus(ctx, p.RunID,
		[]int{models.AuditStatusCrawling}, models.AuditStatusCollecting,
		bson.M{"collectors_expected": expected}); err != nil {
		return fmt.Errorf("audit crawl: mark collecting: %w", err)
	}

	return s.pipeline.DispatchNext(ctx, audit.ProcessAuditCrawl, dc)
}

// resolveRunLocation picks the run's ranking market, strongest signal first:
// the tenant WebEntity's SIE location setting, the target's ccTLD, then the
// homepage's <html lang> region. 0 = no signal (collectors default to US).
func (s *auditService) resolveRunLocation(ctx context.Context, run *models.AuditRun) int {
	if run.WebEntityID != nil {
		found, entity, err := models.FindWebEntityByID(ctx, run.WebEntityID.Hex())
		if err != nil {
			log.Warn("audit crawl: web entity load for location failed", "runId", run.ID.Hex(), "error", err)
		} else if found && entity.LocationCode > 0 {
			return entity.LocationCode
		}
	}
	if loc := collectors.InferLocationFromDomain(s.locations, run.TargetDomain); loc != 0 {
		return loc
	}
	if lang := collectors.ProbeHomepageLang(ctx, run.TargetDomain); lang != "" {
		return collectors.InferLocationFromLang(s.locations, lang)
	}
	return 0
}

func (s *auditService) postOnPageTask(ctx context.Context, run *models.AuditRun) (string, error) {
	resp, err := s.dfs.PostOnPageTask(ctx, dto.OnPageTaskPostRequest{Tasks: []dto.OnPageTaskPostTask{{
		Target:           run.TargetDomain,
		MaxCrawlPages:    run.PageCap,
		EnableJavascript: true,
		StoreRawHTML:     false,
		LoadResources:    false,
	}}})
	if err != nil {
		return "", fmt.Errorf("audit crawl: task_post: %w", err)
	}
	if len(resp.Tasks) == 0 || resp.Tasks[0].ID == "" {
		return "", fmt.Errorf("audit crawl: task_post returned no task id")
	}
	return resp.Tasks[0].ID, nil
}

// pollOnPageSummary loops GET on_page/summary/{id} until the crawl reports
// finished or the poll window closes. A window close returns a RETRYABLE
// error — the redelivery re-enters with the stored task id and keeps
// polling, so a slow crawl gets attempts × window before the wrapper stamps
// the timeout.
func (s *auditService) pollOnPageSummary(ctx context.Context, taskID string) (*dto.OnPageSummaryResult, error) {
	interval := time.Duration(s.values.Crawl.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	timeout := time.Duration(s.values.Crawl.PollTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	deadline := time.Now().Add(timeout)

	for {
		resp, err := s.dfs.GetOnPageSummary(ctx, taskID)
		if err != nil {
			log.Warn("audit crawl: summary poll failed, will retry", "taskId", taskID, "error", err)
		} else if len(resp.Tasks) > 0 && len(resp.Tasks[0].Result) > 0 {
			result := &resp.Tasks[0].Result[0]
			if result.CrawlProgress == "finished" {
				return result, nil
			}
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("audit crawl: poll window closed before the crawl finished [%s]", audit.AuditReasonTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("audit crawl: context canceled during poll: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}
