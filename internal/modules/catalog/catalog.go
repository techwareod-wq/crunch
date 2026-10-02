// Package catalog is the WarehouseHub catalog module (spec 03): warehouses
// and their revision lifecycle (draft → review → live → archived), rent
// terms, media uploads, geocoding, slugs and the public listing API.
package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

const systemUserID = "system"

// Module is the catalog feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext

	store  Store
	rules  domain.Rules
	search domain.SearchEngine
	svc    *Service
	media  *MediaService
	public *Public
}

var _ modules.Module = (*Module)(nil)

// New builds the module. rules is the attributes snapshot (02). appCtx is
// read at call time, never here.
func New(appCtx *config.AppContext, rules domain.Rules) *Module {
	m := &Module{appCtx: appCtx, store: mongoStore{}, rules: rules}
	m.svc = &Service{
		store:    m.store,
		rules:    rules,
		log:      changelog.New(),
		geocoder: func() interfaces.Geocoder { return m.appCtx.Geocoder },
		dispatch: m.dispatchKeyed,
		cfg:      m.cfg,
		now:      time.Now,
	}
	m.media = &MediaService{
		store: m.store,
		s3:    func() interfaces.S3 { return m.appCtx.S3Provider },
		aws:   func() config.AWSConfig { return m.appCtx.Config.AWS },
		links: func() config.StorageValues { return m.appCtx.Config.Values.Storage },
		cfg:   m.cfg,
		now:   time.Now,
	}
	m.public = &Public{
		store:     m.store,
		rules:     rules,
		search:    func() domain.SearchEngine { return m.search },
		mediaBase: func() string { return m.appCtx.Config.AWS.PublicMediaBaseURL },
		siteBase:  func() string { return m.appCtx.Config.Values.WarehouseHub.PublicBaseURL },
		cfg:       m.cfg,
		logWarn:   func(msg string, err error) { log.Warn(msg, "error", err) },
	}
	return m
}

func (m *Module) Name() string { return "catalog" }

// SetSearchEngine wires search (04) for the 410 page's nearby list.
func (m *Module) SetSearchEngine(e domain.SearchEngine) { m.search = e }

func (m *Module) cfg() config.CatalogValues { return m.appCtx.Config.Values.WarehouseHub.Catalog }

func (m *Module) dispatchKeyed(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error {
	d := m.appCtx.InternalServices.Dispatcher
	if d == nil {
		return fmt.Errorf("dispatcher not wired")
	}
	return d.DispatchKeyed(ctx, string(pt), systemUserID, key, payload)
}

func (m *Module) EnsureIndexes(ctx context.Context) error { return ensureIndexes(ctx) }

func (m *Module) RegisterHandlers(reg asynchandler.Registry) {
	reg.Register(ProcessGeocodeRetry, asynchandler.Typed(func(ctx context.Context, _ string, p GeocodeRetryPayload) error {
		return m.svc.geocodeRetry(ctx, p)
	}))
	reg.Register(ProcessMediaGC, asynchandler.Typed(func(ctx context.Context, _ string, _ struct{}) error {
		return m.media.GC(ctx)
	}))
}

func (m *Module) CronJobs() []cron.Job {
	return []cron.Job{{
		Name:    JobMediaGC,
		Spec:    cron.AtLocal(mediaGCLocalTime),
		Resolve: resolveMediaGC,
		Process: ProcessMediaGC,
		CatchUp: mediaGCCatchUp,
	}}
}
