package analytics

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ProcessAnalyticsReplay is the async process behind admin-triggered
// re-normalization (LLD §6): each unit deletes one (entity, date-chunk)'s
// facts and rebuilds them from stored raw through the SAME normalizeAndStore
// path daily ingest uses.
const ProcessAnalyticsReplay pipeline.ProcessType = "ANALYTICS_REPLAY"

// replayChunkDays is the date-span of one replay unit.
const replayChunkDays = 30

// ReplayPayload is one (entity, date-chunk) unit of replay work.
type ReplayPayload struct {
	ReplayID    string     `json:"replayId"`
	Source      string     `json:"source"`
	WebEntityID string     `json:"webEntityId"`
	Window      DateWindow `json:"window"`
}

// ReplayMaxDate is the newest date a replay may touch: the day BEFORE the
// source's rolling re-fetch window starts (GSC: D−6), so a replay can never
// race the daily cron. Nothing is lost by the clamp — the live window is
// re-normalized with current code by the next daily run anyway.
func ReplayMaxDate(src Source) string {
	return IngestWindow(src).From // window start is D−(LagDays+WindowDays−1)
}

// StartReplay validates + clamps the range, resolves the target entities,
// mints and persists the tracking doc, and enqueues one keyed unit per
// (entity, 30-day chunk). Returns the tracking doc (with the minted replayID).
// Idempotency keys include the replayID, so one replay isn't double-applied
// but a NEW replay of the same range is allowed.
func StartReplay(ctx context.Context, dispatcher interfaces.Dispatcher, source string, entityID *primitive.ObjectID, from, to string) (*models.AnalyticsReplay, error) {
	src, ok := Get(source)
	if !ok {
		return nil, fmt.Errorf("analytics: unknown source %q", source)
	}
	if _, err := time.Parse(DateLayout, from); err != nil {
		return nil, fmt.Errorf("analytics: bad from date %q: %w", from, err)
	}
	if _, err := time.Parse(DateLayout, to); err != nil {
		return nil, fmt.Errorf("analytics: bad to date %q: %w", to, err)
	}
	maxDate := ReplayMaxDate(src)
	if to >= maxDate {
		to = previousDate(maxDate)
	}
	if from > to {
		return nil, fmt.Errorf("analytics: empty replay range after live-window clamp (from %s, clamped to %s)", from, to)
	}

	var entities []*models.WebEntity
	if entityID != nil {
		found, entity, err := models.FindWebEntityByID(ctx, entityID.Hex())
		if err != nil {
			return nil, fmt.Errorf("analytics: load web entity %s: %w", entityID.Hex(), err)
		}
		if !found {
			return nil, fmt.Errorf("analytics: web entity %s not found", entityID.Hex())
		}
		entities = []*models.WebEntity{entity}
	} else {
		var err error
		if src.RequiresConnection() {
			entities, err = models.FindWebEntitiesWithIntegrationStatus(ctx, src.Name(), models.IntegrationStatusConnected)
		} else {
			entities, err = models.FindFinalisedWebEntities(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("analytics: resolve replay entities: %w", err)
		}
	}

	chunks, err := chunkWindow(from, to, replayChunkDays)
	if err != nil {
		return nil, err
	}

	replay := models.AnalyticsReplay{
		ID:         primitive.NewObjectID(),
		Source:     source,
		From:       from,
		To:         to,
		UnitsTotal: len(entities) * len(chunks),
		Status:     models.AnalyticsReplayStatusRunning,
		CreatedAt:  time.Now().UTC(),
	}
	if err := models.InsertAnalyticsReplay(ctx, replay); err != nil {
		return nil, err
	}

	for _, entity := range entities {
		userID, err := models.RepresentativeUserIDForWebEntity(ctx, entity)
		if err != nil {
			// Count the unresolvable unit as failed up front rather than
			// leaving the tracking doc waiting on a unit that never dispatched.
			log.Error("analytics: replay unit skipped, no representative user",
				"replayId", replay.ID.Hex(), "webEntityId", entity.ID.Hex(), "error", err)
			for range chunks {
				if recErr := models.RecordAnalyticsReplayUnit(ctx, replay.ID, true); recErr != nil {
					log.Error("analytics: recording skipped replay unit failed", "error", recErr)
				}
			}
			continue
		}
		for _, chunk := range chunks {
			key := fmt.Sprintf("analytics_replay:%s:%s:%s", replay.ID.Hex(), entity.ID.Hex(), chunk.From)
			payload := ReplayPayload{
				ReplayID:    replay.ID.Hex(),
				Source:      source,
				WebEntityID: entity.ID.Hex(),
				Window:      chunk,
			}
			if err := dispatcher.DispatchKeyed(ctx, string(ProcessAnalyticsReplay), userID, key, payload); err != nil {
				log.Error("analytics: replay unit dispatch failed",
					"replayId", replay.ID.Hex(), "webEntityId", entity.ID.Hex(), "chunk", chunk.From, "error", err)
				if recErr := models.RecordAnalyticsReplayUnit(ctx, replay.ID, true); recErr != nil {
					log.Error("analytics: recording failed replay dispatch failed", "error", recErr)
				}
			}
		}
	}

	found, updated, err := models.FindAnalyticsReplayByID(ctx, replay.ID)
	if err == nil && found {
		return updated, nil
	}
	return &replay, nil
}

// ProcessReplayChunk is one replay unit: delete the chunk's facts (facts whose
// dims vanish under new normalizer logic must not linger), rebuild them from
// raw via normalizeAndStore, and report the unit's outcome to the tracking
// doc. finalAttempt marks the last delivery of this unit — only then is a
// failure recorded, so SQS retries don't double-count.
func ProcessReplayChunk(ctx context.Context, p ReplayPayload, finalAttempt bool) error {
	replayID, err := primitive.ObjectIDFromHex(p.ReplayID)
	if err != nil {
		return fmt.Errorf("analytics: bad replay id %q: %w", p.ReplayID, err)
	}

	err = replayChunk(ctx, p)
	if err == nil {
		if recErr := models.RecordAnalyticsReplayUnit(ctx, replayID, false); recErr != nil {
			log.Error("analytics: recording replay unit success failed",
				"replayId", p.ReplayID, "webEntityId", p.WebEntityID, "error", recErr)
		}
		return nil
	}
	if finalAttempt {
		if recErr := models.RecordAnalyticsReplayUnit(ctx, replayID, true); recErr != nil {
			log.Error("analytics: recording replay unit failure failed",
				"replayId", p.ReplayID, "webEntityId", p.WebEntityID, "error", recErr)
		}
	}
	return err
}

func replayChunk(ctx context.Context, p ReplayPayload) error {
	src, ok := Get(p.Source)
	if !ok {
		return fmt.Errorf("analytics: unknown source %q", p.Source)
	}
	found, entity, err := models.FindWebEntityByID(ctx, p.WebEntityID)
	if err != nil {
		return fmt.Errorf("analytics: load web entity %s: %w", p.WebEntityID, err)
	}
	if !found {
		return fmt.Errorf("analytics: web entity %s not found", p.WebEntityID)
	}

	deleted, err := models.DeleteAnalyticsFactsForRange(ctx, entity.ID, src.Name(), p.Window.From, p.Window.To)
	if err != nil {
		return err
	}
	log.Info("analytics: replay chunk rebuilding",
		"replayId", p.ReplayID, "source", p.Source, "webEntityId", p.WebEntityID,
		"window", p.Window.From+".."+p.Window.To, "deletedFacts", deleted)
	return normalizeAndStore(ctx, src, entity, p.Window)
}

// chunkWindow splits an inclusive [from, to] range into consecutive windows of
// at most days each.
func chunkWindow(from, to string, days int) ([]DateWindow, error) {
	start, err := time.Parse(DateLayout, from)
	if err != nil {
		return nil, fmt.Errorf("analytics: bad chunk from %q: %w", from, err)
	}
	end, err := time.Parse(DateLayout, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: bad chunk to %q: %w", to, err)
	}
	var chunks []DateWindow
	for cur := start; !cur.After(end); cur = cur.AddDate(0, 0, days) {
		chunkEnd := cur.AddDate(0, 0, days-1)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		chunks = append(chunks, DateWindow{
			From: cur.Format(DateLayout),
			To:   chunkEnd.Format(DateLayout),
		})
	}
	return chunks, nil
}

// previousDate steps one day back from a DateLayout string.
func previousDate(date string) string {
	d, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, -1).Format(DateLayout)
}
