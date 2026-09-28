package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// backfillChunkDays is the date-span of one backfill unit. 30 days × 4 pulls =
// 120 sequential API calls per unit — comfortably inside one async delivery.
const backfillChunkDays = 30

// BackfillWindow is the historical range one backfill covers:
// [settled − (BackfillDays−1) … the day before the live re-fetch window]. The
// live window ([D−5..D−2] for GSC) is owned by the daily cron and the
// verify-triggered first ingest, so backfill stops right below it — same
// no-race rule as ReplayMaxDate. ok is false when the source declares no
// backfill (BackfillDays 0) or the range collapses.
func BackfillWindow(src Source) (DateWindow, bool) {
	days := src.Schedule().BackfillDays
	if days <= 0 {
		return DateWindow{}, false
	}
	settled, err := time.Parse(DateLayout, LatestSettledDate(src))
	if err != nil {
		return DateWindow{}, false
	}
	to := previousDate(IngestWindow(src).From)
	from := settled.AddDate(0, 0, -(days - 1)).Format(DateLayout)
	if from > to {
		return DateWindow{}, false
	}
	return DateWindow{From: from, To: to}, true
}

// StartBackfill enqueues one shared ANALYTICS_INGEST unit per 30-day chunk of
// the source's backfill window for ONE entity — backfill is ingest over old
// dates, not a new process: raw upserts and fact rebuilds are idempotent, so
// overlapping an existing partial history is safe. Chunks dispatch NEWEST
// FIRST so the dashboard's recent ranges fill before deep history. Returns the
// window and the number of units dispatched.
//
// Idempotency keys embed the chunk bounds (which shift with the settled
// date), so a same-day double-trigger dedupes and a later re-trigger
// re-fetches. A non-empty nonce is appended to every key, opting OUT of that
// dedupe — the admin endpoint passes one so a manual "run it again" can never
// silently no-op against an earlier same-day run (2026-08-25: the re-verify
// after the quota storm deduped all 16 chunks and healed nothing). Connect-time
// backfills pass "" and keep the date-scoped dedupe.
func StartBackfill(ctx context.Context, dispatcher interfaces.Dispatcher, src Source, entity *models.WebEntity, userID, nonce string) (DateWindow, int, error) {
	window, ok := BackfillWindow(src)
	if !ok {
		return DateWindow{}, 0, fmt.Errorf("analytics: source %q has no backfill window", src.Name())
	}
	chunks, err := chunkWindow(window.From, window.To, backfillChunkDays)
	if err != nil {
		return DateWindow{}, 0, err
	}

	units := 0
	for i := len(chunks) - 1; i >= 0; i-- {
		chunk := chunks[i]
		key := fmt.Sprintf("analytics_backfill:%s:%s:%s:%s", src.Name(), entity.ID.Hex(), chunk.From, chunk.To)
		if nonce != "" {
			key += ":" + nonce
		}
		payload := IngestPayload{Source: src.Name(), WebEntityID: entity.ID.Hex(), Window: chunk}
		if err := dispatcher.DispatchKeyed(ctx, string(ProcessAnalyticsIngest), userID, key, payload); err != nil {
			// Report the partial dispatch — the caller decides whether that
			// blocks anything; every dispatched unit still runs.
			return window, units, fmt.Errorf("analytics: backfill dispatch %s chunk %s: %w", src.Name(), chunk.From, err)
		}
		units++
	}
	log.Info("analytics: backfill dispatched",
		"source", src.Name(), "webEntityId", entity.ID.Hex(),
		"window", window.From+".."+window.To, "units", units)
	return window, units, nil
}
