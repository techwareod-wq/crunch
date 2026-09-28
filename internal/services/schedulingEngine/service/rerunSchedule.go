package service

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/utils"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// RerunSchedule is the SE_RERUN_SCHEDULE handler — the periodic re-run of the
// scheduling engine, intended to be dispatched when a subscription renews
// (trigger not wired yet). It appends one full-mode window — the WebEntity's
// articles_per_week × scheduling.weeks — of new calendar rows starting the day
// after the latest existing slot, so a calendar that ran through June 10th
// continues on June 11th. When the calendar end is already in the past (late
// renewal), the window starts today instead — a rerun never backfills
// past-dated articles. Existing rows are untouched; article-type generation is
// fanned out for the new rows only, and the WEC's status is left alone (it is
// already SchedulingDone, and markSchedulingDoneIfComplete no-ops there).
//
// Idempotency: duplicate deliveries of a completed message are absorbed by the
// asyncHandler dedupe ledger. Error-retries re-run the handler with the same
// rerunKey (the queue MessageID); persisting that key with the window anchor
// before the insert lets a retry resume filling the same window — recount,
// fill only the remainder, re-dispatch fan-out the crashed attempt lost —
// instead of opening a second window off the moved calendar end. The
// not-yet-scheduled candidate filter keeps this bulk path from double-booking
// a keyword (manual reuse is allowed, but auto-scheduling never reuses).
// All of this covers retries of ONE
// message: two distinct dispatches append two windows by design, so the
// renewal trigger must dedupe per renewal period (see the process-type const).
func (s *schedulingEngineService) RerunSchedule(ctx context.Context, userId string, payload se.SEOrchestratePayload, rerunKey string) error {
	ok, wec, err := s.store.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("rerun schedule: get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("rerun schedule: web entity context not found: %s", payload.WebEntityContextID)
	}
	// The previous scheduling run must have finished — this also defers a rerun
	// that races the trial→paid upgrade re-run (status is rewound while
	// upgrade_state=="expanding" and only restored on finalize).
	if wec.EffectiveStatus() < models.SIEStatusSchedulingDone {
		return fmt.Errorf("rerun schedule: scheduling not complete for %s (status %d)", payload.WebEntityContextID, wec.EffectiveStatus())
	}
	// Renewal is a paid-plan event: a trial WEC must never receive a rerun —
	// its calendar is the fixed trial week, and expansion happens via
	// SE_EXTEND_SCHEDULE on upgrade (which also flips sie_mode to full).
	if wec.EffectiveSIEMode() != models.SIEModeFull {
		return fmt.Errorf("rerun schedule: WEC %s is in trial mode", payload.WebEntityContextID)
	}

	ok, we, err := s.store.GetWebEntityByID(ctx, payload.WebEntityID)
	if err != nil {
		return fmt.Errorf("rerun schedule: get web entity: %w", err)
	}
	if !ok {
		return fmt.Errorf("rerun schedule: web entity not found: %s", payload.WebEntityID)
	}

	// Always the full-mode cadence: a rerun means the subscription renewed, so
	// the user's chosen articles_per_week applies regardless of how the original
	// calendar was sized.
	cadence, err := resolveCadence(we)
	if err != nil {
		return fmt.Errorf("rerun schedule: %w", err)
	}
	windowSlots, err := utils.SlotsForWindow(cadence, s.values.Weeks)
	if err != nil {
		return fmt.Errorf("rerun schedule: compute window slots: %w", err)
	}

	existing, err := s.store.GetScheduledArticlesByWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("rerun schedule: load existing rows: %w", err)
	}

	resuming := rerunKey != "" && wec.ProcessMetadata.SchedulingMetadata.RerunKey == rerunKey
	var anchor time.Time
	if resuming {
		anchor = wec.ProcessMetadata.SchedulingMetadata.RerunAnchor
	} else {
		anchor = nextWindowAnchor(existing)
	}

	inWindow := 0
	for _, sa := range existing {
		if !sa.ScheduleDate.Before(anchor) {
			inWindow++
		}
	}
	need := windowSlots - inWindow

	if resuming {
		// A previous attempt of this same message may have crashed between its
		// insert and its fan-out loop — a partial insert leaves rows no later
		// path would ever dispatch. Sweep them before doing anything else.
		if err := s.redispatchUntypedRows(ctx, userId, payload.WebEntityContextID, existing, anchor); err != nil {
			return err
		}
	}
	if need <= 0 {
		// The window insert already completed; only the total stamp can still
		// be outstanding.
		if err := s.store.SetSchedulingTotal(ctx, payload.WebEntityContextID, len(existing)); err != nil {
			return fmt.Errorf("rerun schedule: set scheduling total: %w", err)
		}
		log.Info("rerun schedule: window already filled by a prior attempt",
			"webEntityContextId", payload.WebEntityContextID)
		return nil
	}

	// Candidates = highest-OS keywords without a calendar row yet.
	candidates, keywordByID, err := s.unscheduledCandidates(ctx, payload.WebEntityContextID, existing)
	if err != nil {
		return fmt.Errorf("rerun schedule: %w", err)
	}
	if len(candidates) == 0 {
		log.Warn("rerun schedule: no unscheduled keywords left — nothing to append",
			"webEntityContextId", payload.WebEntityContextID)
		if resuming {
			// The partial rows a previous attempt inserted are all this window
			// will ever hold; make the total stamp reflect them.
			if err := s.store.SetSchedulingTotal(ctx, payload.WebEntityContextID, len(existing)); err != nil {
				return fmt.Errorf("rerun schedule: set scheduling total: %w", err)
			}
		}
		return nil
	}

	// Regenerate the window's full slot list from the anchor and take the
	// unfilled tail — slot dates are deterministic given cadence + anchor, so a
	// resumed retry fills exactly the slots its first attempt missed.
	slots, err := utils.GenerateSlots(cadence, anchor, windowSlots)
	if err != nil {
		return fmt.Errorf("rerun schedule: generate slots: %w", err)
	}
	slots = slots[windowSlots-need:]

	// The cluster/day constraint is seeded from the existing rows — a resumed
	// retry on a multiple-per-day cadence can land on a day that already holds
	// rows.
	assignments := assignKeywordsToSlotsSeeded(candidates, slots, seedUsedClusters(existing, keywordByID))
	if len(assignments) == 0 {
		return fmt.Errorf("rerun schedule: produced zero assignments")
	}

	// Persist the resume marker before inserting: if the insert or anything
	// after it fails, the retry of this same message re-enters through the
	// marker branch above and fills only the remainder.
	if !resuming {
		if err := s.store.SetSchedulingRerunMarker(ctx, payload.WebEntityContextID, rerunKey, anchor); err != nil {
			return fmt.Errorf("rerun schedule: persist rerun marker: %w", err)
		}
	}

	docs, err := s.persistAndFanOut(ctx, userId, payload, we, assignments, len(existing)+len(assignments))
	if err != nil {
		return fmt.Errorf("rerun schedule: %w", err)
	}

	log.Info("schedule rerun appended", "webEntityContextId", payload.WebEntityContextID,
		"appended", len(docs), "total", len(existing)+len(docs),
		"cadence", cadence, "windowStart", anchor.Format("2006-01-02"))

	return nil
}

// redispatchUntypedRows re-dispatches article-type fan-out for window rows
// whose first dispatch was lost — rows a previous attempt of this rerun
// message inserted before crashing ahead of (or during) its fan-out loop. Only
// rerun-inserted rows can be untyped in-window: manual slots are created with
// a user-chosen type. Re-dispatching a row whose first fan-out is merely still
// in flight is harmless — GenerateArticleType and GenerateTitle both write
// idempotent field values.
func (s *schedulingEngineService) redispatchUntypedRows(ctx context.Context, userId, webEntityContextID string, existing []*models.ScheduledArticle, anchor time.Time) error {
	redispatched := 0
	for _, sa := range existing {
		if sa.ScheduleDate.Before(anchor) || sa.ArticleType != "" {
			continue
		}
		msg := se.SEArticleStepPayload{ScheduledArticleID: sa.ID.Hex()}
		if err := s.dispatcher.Dispatch(ctx, string(se.ProcessSchedulingGenerateArticleType), userId, msg); err != nil {
			return fmt.Errorf("rerun schedule: re-dispatch article-type fan-out for %s: %w", sa.ID.Hex(), err)
		}
		redispatched++
	}
	if redispatched > 0 {
		log.Info("rerun schedule: re-dispatched lost fan-outs",
			"webEntityContextId", webEntityContextID, "redispatched", redispatched)
	}
	return nil
}

// DispatchRerunSchedule is the synchronous entry point for a rerun — the admin
// console's manual trigger until the renewal webhook is wired. It validates
// what the queue handler can't surface to the caller (the dispatch returns 202
// before the handler runs) and enqueues SE_RERUN_SCHEDULE; the handler derives
// its rerunKey from the queue MessageID as usual. Missing and unauthorised
// collapse into ErrWebEntityContextNotFound so callers can't probe for another
// user's context.
func (s *schedulingEngineService) DispatchRerunSchedule(ctx context.Context, userId, webEntityContextID string) error {
	ok, wec, err := s.store.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("dispatch rerun schedule: get web entity context: %w", err)
	}
	if !ok || wec.UserID.Hex() != userId {
		return se.ErrWebEntityContextNotFound
	}
	if wec.EffectiveStatus() < models.SIEStatusSchedulingDone {
		return se.ErrSchedulingNotComplete
	}

	payload := se.SEOrchestratePayload{
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextID,
	}
	if err := s.dispatcher.Dispatch(ctx, string(se.ProcessSchedulingRerunSchedule), userId, payload); err != nil {
		return fmt.Errorf("dispatch rerun schedule: %w", err)
	}
	return nil
}

// nextWindowAnchor returns the first calendar day a fresh rerun may schedule
// on: the day after the latest existing slot, pulled forward to today (UTC)
// when the calendar ended in the past. Today when no rows exist at all.
func nextWindowAnchor(existing []*models.ScheduledArticle) time.Time {
	now := time.Now().UTC()
	anchor := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, sa := range existing {
		d := sa.ScheduleDate.UTC()
		next := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		if next.After(anchor) {
			anchor = next
		}
	}
	return anchor
}
