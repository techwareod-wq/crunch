// Package attributes is the WarehouseHub attributes module (spec 02): the
// admin-defined attribute tree (nodes + fields) and industry rules, the
// Warehouse root bootstrap, the in-memory rules snapshot other modules
// evaluate with (domain.Rules), and the recompute jobs that keep every live
// warehouse's search projection in step with the rules.
// The evaluator itself is pure and lives in internal/warehousehub/domain.
package attributes

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/pipeline"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// defaultRefresh is used when values leave cacheRefreshSeconds unset.
const defaultRefresh = time.Minute

// Module is the attributes feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext

	store Store
	cache *Cache
	svc   *Service
	rc    *recomputer
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	m := &Module{appCtx: appCtx, store: mongoStore{}}
	m.cache = NewCache(m.store.Load)
	m.svc = &Service{
		store: m.store,
		cache: m.cache,
		log:   changelog.New(),
		dispatch: func(ctx context.Context, v int64) error {
			return m.dispatchKeyed(ctx, ProcessRecomputeAll, allKey(v, runRules), RecomputeAllPayload{RulesVersion: v, Run: runRules})
		},
		now: time.Now,
	}
	m.rc = &recomputer{
		rules:         m.cache,
		warehouses:    mongoWarehouses{},
		rulesVersion:  m.store.RulesVersion,
		dispatchKeyed: m.dispatchKeyed,
		batchSize:     func() int { return m.appCtx.Config.Values.WarehouseHub.Attributes.RecomputeBatchSize },
		now:           time.Now,
	}
	return m
}

func (m *Module) Name() string { return "attributes" }

// Rules is the snapshot other modules evaluate with (wired in
// cmd/service/modules.go).
func (m *Module) Rules() domain.Rules { return m.cache }

func (m *Module) dispatchKeyed(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error {
	d := m.appCtx.InternalServices.Dispatcher
	if d == nil {
		return fmt.Errorf("dispatcher not wired")
	}
	return d.DispatchKeyed(ctx, string(pt), systemUserID, key, payload)
}

func (m *Module) EnsureIndexes(ctx context.Context) error { return ensureIndexes(ctx) }

// Start ensures the Warehouse root exists (D-142), boot-loads the rules
// snapshot and starts the refresh ticker. There is no starter tree (D-135):
// admins build everything below the root.
func (m *Module) Start(ctx context.Context) error {
	if err := m.svc.EnsureRoot(ctx); err != nil {
		return fmt.Errorf("attributes root bootstrap: %w", err)
	}
	if err := m.cache.Reload(ctx); err != nil {
		return fmt.Errorf("attributes cache boot load: %w", err)
	}
	snap := m.cache.Snapshot()
	refresh := time.Duration(m.appCtx.Config.Values.WarehouseHub.Attributes.CacheRefreshSeconds) * time.Second
	if refresh <= 0 {
		refresh = defaultRefresh
	}
	m.cache.StartRefresh(ctx, refresh)
	log.Info("attributes cache loaded", "rules_version", snap.Version, "nodes", len(snap.Nodes), "industries", len(snap.Industries))
	return nil
}

func (m *Module) RegisterHandlers(reg asynchandler.Registry) {
	reg.Register(ProcessRecomputeAll, asynchandler.Typed(func(ctx context.Context, _ string, p RecomputeAllPayload) error {
		return m.rc.all(ctx, p)
	}))
	reg.Register(ProcessRecomputeBatch, asynchandler.Typed(func(ctx context.Context, _ string, p RecomputeBatchPayload) error {
		return m.rc.batch(ctx, p)
	}))
}

func (m *Module) CronJobs() []cron.Job {
	return []cron.Job{{
		Name:    JobRecomputeSafetyNet,
		Spec:    cron.AtLocal(safetyNetLocalTime),
		Resolve: m.rc.resolveSafetyNet,
		Process: ProcessRecomputeAll,
		CatchUp: safetyNetCatchUp,
	}}
}
