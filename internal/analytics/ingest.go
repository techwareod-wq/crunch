package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ProcessAnalyticsIngest is the single async process behind EVERY source's
// cron beat and the verify endpoint's immediate first ingest — the source name
// travels in the payload, so new sources need no new handler registration.
const ProcessAnalyticsIngest pipeline.ProcessType = "ANALYTICS_INGEST"

// IngestPayload is one (source, entity, window) unit of ingest work.
type IngestPayload struct {
	Source      string     `json:"source"`
	WebEntityID string     `json:"webEntityId"`
	Window      DateWindow `json:"window"`
}

// ProcessIngest is the shared ingest orchestrator (LLD §3.1):
//
//  1. resolve source + entity (either gone ⇒ logged no-op, not a retry)
//  2. guard: connection-requiring sources need integrations.<source>.status
//     "connected"; connection-less sources need a finalised entity
//  3. fetch the window's pulls — an ErrAuthRevoked failure flips the
//     integration to "error" and completes WITHOUT retry; any other fetch
//     error fails the unit so SQS retries it whole (raw upserts are
//     idempotent, so no partial-day facts)
//  4. upsert pulls into analyticsRaw (the replay tier)
//  5. normalizeAndStore — the exact function replay reuses
//  6. stamp integrations.<source>.last_sync_at
func ProcessIngest(ctx context.Context, p IngestPayload) error {
	src, ok := Get(p.Source)
	if !ok {
		return fmt.Errorf("analytics: unknown source %q", p.Source)
	}
	found, entity, err := models.FindWebEntityByID(ctx, p.WebEntityID)
	if err != nil {
		return fmt.Errorf("analytics: load web entity %s: %w", p.WebEntityID, err)
	}
	if !found {
		log.Info("analytics: ingest unit for missing web entity, skipping",
			"source", p.Source, "webEntityId", p.WebEntityID)
		return nil
	}

	if src.RequiresConnection() {
		state, ok := entity.Integration(src.Name())
		if !ok || state.Status != models.IntegrationStatusConnected {
			log.Info("analytics: source not connected, skipping ingest",
				"source", p.Source, "webEntityId", p.WebEntityID, "status", state.Status)
			return nil
		}
	} else if !entity.Finalised {
		log.Info("analytics: entity not finalised, skipping connection-less ingest",
			"source", p.Source, "webEntityId", p.WebEntityID)
		return nil
	}

	pulls, err := src.FetchWindow(ctx, entity, p.Window)
	if err != nil {
		if errors.Is(err, ErrAuthRevoked) {
			log.Warn("analytics: source access revoked, marking integration errored",
				"source", p.Source, "webEntityId", p.WebEntityID, "error", err)
			if setErr := models.SetWebEntityIntegrationFields(ctx, entity.ID, src.Name(), bson.M{
				"status":     models.IntegrationStatusError,
				"last_error": err.Error(),
			}); setErr != nil {
				log.Error("analytics: failed to record integration error state",
					"source", p.Source, "webEntityId", p.WebEntityID, "error", setErr)
			}
			return nil // deliberate: retrying can't fix a revoked grant
		}
		return fmt.Errorf("analytics: fetch %s window %s..%s: %w", p.Source, p.Window.From, p.Window.To, err)
	}

	raws := make([]models.AnalyticsRaw, 0, len(pulls))
	now := time.Now().UTC()
	for _, pull := range pulls {
		raws = append(raws, models.AnalyticsRaw{
			Source:      src.Name(),
			WebEntityID: entity.ID,
			CompanyID:   entity.CompanyID,
			Date:        pull.Date,
			Pull:        pull.Pull,
			FetchedAt:   now,
			RowCount:    pull.RowCount,
			Payload:     pull.Payload,
		})
	}
	if err := models.UpsertAnalyticsRaws(ctx, raws); err != nil {
		return fmt.Errorf("analytics: store raw %s: %w", p.Source, err)
	}

	if err := normalizeAndStore(ctx, src, entity, p.Window); err != nil {
		return err
	}

	if err := models.SetWebEntityIntegrationFields(ctx, entity.ID, src.Name(), bson.M{
		"last_sync_at": now,
	}); err != nil {
		log.Error("analytics: failed to stamp last_sync_at",
			"source", p.Source, "webEntityId", p.WebEntityID, "error", err)
	}
	return nil
}

// normalizeAndStore is ingest steps 5–7 and THE function replay calls (replay
// reads raw from Mongo instead of fetching, then rebuilds facts through this
// exact path): resolve the NormalizeContext once, normalize each raw doc
// (recovering per-doc panics — one poisoned payload must not sink the run;
// it's replayable after a fix), fold collisions per the source's MergePolicy,
// stamp the envelope, bulk-upsert.
func normalizeAndStore(ctx context.Context, src Source, entity *models.WebEntity, window DateWindow) error {
	nctx, err := buildNormalizeContext(ctx, entity)
	if err != nil {
		return fmt.Errorf("analytics: build normalize context: %w", err)
	}

	raws, err := models.FindAnalyticsRawForRange(ctx, entity.ID, src.Name(), window.From, window.To)
	if err != nil {
		return fmt.Errorf("analytics: read raw for normalize: %w", err)
	}

	var facts []models.AnalyticsFact
	for i := range raws {
		docFacts, err := normalizeOne(ctx, src, &raws[i], nctx)
		if err != nil {
			log.Error("analytics: normalizer failed on raw doc, continuing",
				"source", src.Name(), "webEntityId", entity.ID.Hex(),
				"date", raws[i].Date, "pull", raws[i].Pull, "error", err)
			continue
		}
		facts = append(facts, docFacts...)
	}

	merged := mergeFacts(facts, src.MergePolicy())
	for i := range merged {
		merged[i].WebEntityID = entity.ID
		merged[i].CompanyID = entity.CompanyID
		merged[i].Source = src.Name()
	}
	if err := models.BulkUpsertAnalyticsFacts(ctx, merged); err != nil {
		return fmt.Errorf("analytics: store facts %s: %w", src.Name(), err)
	}
	return nil
}

// normalizeOne wraps a single Normalize call with panic recovery.
func normalizeOne(ctx context.Context, src Source, raw *models.AnalyticsRaw, nctx NormalizeContext) (facts []models.AnalyticsFact, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("normalizer panic: %v", r)
		}
	}()
	return src.Normalize(ctx, raw, nctx)
}

// buildNormalizeContext resolves the per-run lookups: the entity's WEC ids →
// the published normalized-URL → scheduledArticle map (GSC article stamping).
func buildNormalizeContext(ctx context.Context, entity *models.WebEntity) (NormalizeContext, error) {
	wecIDs, err := models.FindWebEntityContextIDsByWebEntityID(ctx, entity.ID)
	if err != nil {
		return NormalizeContext{}, fmt.Errorf("resolve web entity context ids: %w", err)
	}
	urlIndex, err := models.FindPublishedNormalizedURLIndex(ctx, wecIDs)
	if err != nil {
		return NormalizeContext{}, fmt.Errorf("resolve article url index: %w", err)
	}
	slugIndex, err := models.FindPublishedSlugIndex(ctx, wecIDs)
	if err != nil {
		return NormalizeContext{}, fmt.Errorf("resolve article slug index: %w", err)
	}
	return NormalizeContext{ArticleURLIndex: urlIndex, ArticleSlugIndex: slugIndex}, nil
}
