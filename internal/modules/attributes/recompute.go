package attributes

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Recompute (spec 02, D-040): every rules write dispatches recompute_all,
// which pages the stale live/archived warehouses and fans out
// recompute_batch messages; each batch evaluates and BulkWrites projections.
const (
	ProcessRecomputeAll   pipeline.ProcessType = "attributes.recompute_all"
	ProcessRecomputeBatch pipeline.ProcessType = "attributes.recompute_batch"
	// JobRecomputeSafetyNet re-runs recompute_all nightly for anything still
	// lagging (values cron.jobs.attributes_recompute_safety_net, off by
	// default).
	JobRecomputeSafetyNet cron.JobName = "attributes_recompute_safety_net"

	systemUserID       = "system"
	defaultBatchSize   = 200
	runRules           = "rules"
	safetyNetCatchUp   = 6 * time.Hour
	safetyNetLocalTime = "03:30"
)

// RecomputeAllPayload is the attributes.recompute_all body. Run separates the
// message keys of a rules-write fan-out from a safety-net run at the same
// rulesVersion.
type RecomputeAllPayload struct {
	RulesVersion int64  `json:"rulesVersion"`
	Run          string `json:"run"`
}

// RecomputeBatchPayload is the attributes.recompute_batch body.
type RecomputeBatchPayload struct {
	RulesVersion int64    `json:"rulesVersion"`
	IDs          []string `json:"ids"`
}

type recomputer struct {
	rules        domain.Rules
	warehouses   WarehouseStore
	rulesVersion func(ctx context.Context) (int64, error)
	// dispatchKeyed enqueues with a stable message id. The key always carries
	// the rulesVersion (00 Async: a bare id would swallow later dispatches).
	dispatchKeyed func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error
	batchSize     func() int
	now           func() time.Time
}

func allKey(v int64, run string) string { return fmt.Sprintf("recompute_all:%d:%s", v, run) }

func batchKey(v int64, run string, n int) string {
	return fmt.Sprintf("recompute:%d:%s:%d", v, run, n)
}

// all is the recompute_all handler. A message for an older rulesVersion is
// dropped: the newer write already dispatched its own fan-out.
func (r *recomputer) all(ctx context.Context, p RecomputeAllPayload) error {
	cur, err := r.rulesVersion(ctx)
	if err != nil {
		return fmt.Errorf("recompute_all: read rulesVersion: %w", err)
	}
	if p.RulesVersion < cur {
		log.Info("recompute_all superseded", "rules_version", p.RulesVersion, "current", cur)
		return nil
	}
	if p.Run == "" {
		p.Run = runRules
	}
	size := r.batchSize()
	if size <= 0 {
		size = defaultBatchSize
	}
	var after primitive.ObjectID
	batches, total := 0, 0
	for {
		ids, err := r.warehouses.StaleIDs(ctx, p.RulesVersion, after, size)
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
		if err := r.dispatchKeyed(ctx, ProcessRecomputeBatch, batchKey(p.RulesVersion, p.Run, batches),
			RecomputeBatchPayload{RulesVersion: p.RulesVersion, IDs: hex}); err != nil {
			return fmt.Errorf("recompute_all: dispatch batch %d: %w", batches, err)
		}
		after = ids[len(ids)-1]
		if len(ids) < size {
			break
		}
	}
	log.Info("recompute_all dispatched", "rules_version", p.RulesVersion, "run", p.Run, "batches", batches, "warehouses", total)
	return nil
}

// batch is the recompute_batch handler: evaluate with a snapshot at least as
// new as the message, then write guarded by fit_rules_version.
func (r *recomputer) batch(ctx context.Context, p RecomputeBatchPayload) error {
	snap, err := r.rules.SnapshotAtLeast(ctx, p.RulesVersion)
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
	docs, err := r.warehouses.LoadEvalDocs(ctx, ids)
	if err != nil {
		return fmt.Errorf("recompute_batch: load: %w", err)
	}
	now := r.now().UTC()
	out := make(map[primitive.ObjectID]domain.Projection, len(docs))
	for _, d := range docs {
		if d.Live == nil {
			continue
		}
		out[d.ID] = domain.Evaluate(snap, d.EvalInput(), now).Projection
	}
	n, err := r.warehouses.WriteProjections(ctx, out)
	if err != nil {
		return fmt.Errorf("recompute_batch: write: %w", err)
	}
	log.Info("recompute_batch done", "rules_version", snap.Version, "evaluated", len(out), "written", n)
	return nil
}

// resolveSafetyNet enqueues one recompute_all at the current rulesVersion with
// a per-day run id, so its batch keys never collide with the write fan-out.
func (r *recomputer) resolveSafetyNet(ctx context.Context, occ cron.Occurrence) ([]cron.Unit, error) {
	v, err := r.rulesVersion(ctx)
	if err != nil {
		return nil, err
	}
	run := "sn-" + occ.At.UTC().Format("20060102")
	return []cron.Unit{{
		UserID:         systemUserID,
		Payload:        RecomputeAllPayload{RulesVersion: v, Run: run},
		IdempotencyKey: allKey(v, run),
	}}, nil
}
