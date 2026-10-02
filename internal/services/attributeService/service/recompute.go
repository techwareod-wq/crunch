package service

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func batchKey(v int64, run string, n int) string {
	return fmt.Sprintf("recompute:%d:%s:%d", v, run, n)
}

// RecomputeAll pages the stale warehouses and fans out recompute_batch
// messages. A message for an older rulesVersion is dropped: the newer write
// already dispatched its own fan-out.
func (s *svc) RecomputeAll(ctx context.Context, p attributeService.RecomputeAllPayload) error {
	cur, err := s.store.RulesVersion(ctx)
	if err != nil {
		return fmt.Errorf("recompute_all: read rulesVersion: %w", err)
	}
	if p.RulesVersion < cur {
		log.Info("recompute_all superseded", "rules_version", p.RulesVersion, "current", cur)
		return nil
	}
	if p.Run == "" {
		p.Run = attributeService.RunRules
	}
	var after primitive.ObjectID
	batches, total := 0, 0
	for {
		ids, err := s.store.StaleWarehouseIDs(ctx, p.RulesVersion, after, s.batchSize)
		if err != nil {
			return fmt.Errorf("recompute_all: page stale ids: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		batches++
		total += len(ids)
		hex := make([]string, len(ids))
		for i, id := range ids {
			hex[i] = id.Hex()
		}
		if err := s.dispatchKeyed(ctx, attributeService.ProcessRecomputeBatch, batchKey(p.RulesVersion, p.Run, batches),
			attributeService.RecomputeBatchPayload{RulesVersion: p.RulesVersion, IDs: hex}); err != nil {
			return fmt.Errorf("recompute_all: dispatch batch %d: %w", batches, err)
		}
		after = ids[len(ids)-1]
		if len(ids) < s.batchSize {
			break
		}
	}
	log.Info("recompute_all dispatched", "rules_version", p.RulesVersion, "run", p.Run, "batches", batches, "warehouses", total)
	return nil
}

// RecomputeBatch evaluates with a snapshot at least as new as the message,
// then writes guarded by fit_rules_version.
func (s *svc) RecomputeBatch(ctx context.Context, p attributeService.RecomputeBatchPayload) error {
	snap, err := s.cache.SnapshotAtLeast(ctx, p.RulesVersion)
	if err != nil {
		return fmt.Errorf("recompute_batch: %w", err)
	}
	ids := make([]primitive.ObjectID, 0, len(p.IDs))
	for _, h := range p.IDs {
		id, err := primitive.ObjectIDFromHex(h)
		if err != nil {
			return fmt.Errorf("recompute_batch: bad id %q: %w", h, pipeline.ErrPermanent)
		}
		ids = append(ids, id)
	}
	docs, err := s.store.WarehouseEvalDocs(ctx, ids)
	if err != nil {
		return fmt.Errorf("recompute_batch: load: %w", err)
	}
	now := s.now().UTC()
	out := make(map[primitive.ObjectID]models.Projection, len(docs))
	for _, d := range docs {
		if d.Live == nil {
			continue
		}
		out[d.ID] = domain.Evaluate(snap, d.Live.Attributes, now).Projection
	}
	n, err := s.store.WriteProjections(ctx, out)
	if err != nil {
		return fmt.Errorf("recompute_batch: write: %w", err)
	}
	log.Info("recompute_batch done", "rules_version", snap.Version, "evaluated", len(out), "written", n)
	return nil
}
