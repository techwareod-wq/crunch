package service

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/services/attributeService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

const (
	systemUserID     = "system"
	defaultBatchSize = 200
)

var _ attributeService.AttributeService = (*svc)(nil)

// svc performs every tree and industry write: validate against a fresh
// snapshot, CAS-write, record change_log, then bump rulesVersion, reload the
// cache and dispatch the recompute (spec 02, D-040). There are no
// transactions; each step is ordered and a failed tail step is logged — the
// cache ticker and the nightly safety net catch up.
type svc struct {
	store store.Store
	cache *Cache
	log   domain.ChangeLog
	// dispatchKeyed enqueues with a stable message id.
	dispatchKeyed func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error
	batchSize     int
	now           func() time.Time
}

// NewService builds the attribute service.
func NewService(st store.Store, dispatcher interfaces.Dispatcher, changeLog domain.ChangeLog, values config.AttributesValues) attributeService.AttributeService {
	keyed := func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error {
		if dispatcher == nil {
			return fmt.Errorf("dispatcher not wired")
		}
		return dispatcher.DispatchKeyed(ctx, string(pt), systemUserID, key, payload)
	}
	return newService(st, keyed, changeLog, values.RecomputeBatchSize, time.Now)
}

func newService(st store.Store, keyed func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error, changeLog domain.ChangeLog, batchSize int, now func() time.Time) *svc {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &svc{store: st, cache: NewCache(st.Load), log: changeLog, dispatchKeyed: keyed, batchSize: batchSize, now: now}
}

func (s *svc) Boot(ctx context.Context) error {
	if err := s.EnsureRoot(ctx); err != nil {
		return fmt.Errorf("attributes root bootstrap: %w", err)
	}
	if err := s.cache.Reload(ctx); err != nil {
		return fmt.Errorf("attributes cache boot load: %w", err)
	}
	snap := s.cache.Snapshot()
	log.Info("attributes cache loaded", "rules_version", snap.Version, "nodes", len(snap.Nodes), "industries", len(snap.Industries))
	return nil
}

func (s *svc) StartRefresh(ctx context.Context, interval time.Duration) {
	s.cache.StartRefresh(ctx, interval)
}

func (s *svc) Rules() domain.Rules { return s.cache }

func (s *svc) Snapshot(ctx context.Context) (*domain.Snapshot, error) { return s.store.Load(ctx) }

func (s *svc) dispatchRecompute(ctx context.Context, rulesVersion int64) error {
	return s.dispatchKeyed(ctx, attributeService.ProcessRecomputeAll, attributeService.RecomputeAllKey(rulesVersion, attributeService.RunRules),
		attributeService.RecomputeAllPayload{RulesVersion: rulesVersion, Run: attributeService.RunRules})
}
