package service

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/services/catalogService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

const systemUserID = "system"

var _ catalogService.CatalogService = (*svc)(nil)

// NewService builds the catalog service. rules is the attribute snapshot
// (attributeService.Rules()); geocoder and s3 may be nil (geocoding off,
// storage unconfigured).
func NewService(
	st store.Store,
	rules domain.Rules,
	changeLog domain.ChangeLog,
	geocoder interfaces.Geocoder,
	dispatcher interfaces.Dispatcher,
	s3 interfaces.S3,
	aws config.AWSConfig,
	storage config.StorageValues,
	values config.WarehouseHubValues,
) catalogService.CatalogService {
	cfg := func() config.CatalogValues { return values.Catalog }
	s := &svc{
		store:    st,
		rules:    rules,
		log:      changeLog,
		geocoder: func() interfaces.Geocoder { return geocoder },
		dispatch: func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error {
			if dispatcher == nil {
				return fmt.Errorf("dispatcher not wired")
			}
			return dispatcher.DispatchKeyed(ctx, string(pt), systemUserID, key, payload)
		},
		cfg: cfg,
		now: time.Now,
	}
	s.media = &mediaService{
		store: st,
		s3:    func() interfaces.S3 { return s3 },
		aws:   func() config.AWSConfig { return aws },
		links: func() config.StorageValues { return storage },
		cfg:   cfg,
		now:   time.Now,
	}
	s.public = &publicSite{
		store:     st,
		rules:     rules,
		search:    func() domain.SearchEngine { return s.search },
		mediaBase: func() string { return aws.PublicMediaBaseURL },
		siteBase:  func() string { return values.PublicBaseURL },
		cfg:       cfg,
		logWarn:   func(msg string, err error) { log.Warn(msg, "error", err) },
	}
	return s
}

func (s *svc) SetSearchEngine(e domain.SearchEngine) { s.search = e }

// --- admin reads ---

func (s *svc) ListWarehouses(ctx context.Context, f models.WarehouseFilter, page, limit int) ([]models.Warehouse, int64, error) {
	return s.store.ListWarehouses(ctx, f, page, limit)
}

func (s *svc) WarehouseDetail(ctx context.Context, warehouseID primitive.ObjectID) (*dto.WarehouseDetail, error) {
	w, err := s.store.GetWarehouse(ctx, warehouseID)
	if err != nil {
		return nil, err
	}
	media, err := s.store.ListMedia(ctx, warehouseID)
	if err != nil {
		return nil, err
	}
	history, err := s.store.Revisions(ctx, warehouseID)
	if err != nil {
		return nil, err
	}
	out := &dto.WarehouseDetail{Warehouse: w, Media: media, History: history}
	if w.OpenRevisionID != nil {
		if rev, err := s.store.GetRevision(ctx, *w.OpenRevisionID); err == nil {
			out.OpenRevision = rev
			out.Preview = s.preview(s.rules.Snapshot(), rev.Content, media)
		}
	}
	return out, nil
}

func (s *svc) GetRevision(ctx context.Context, revisionID primitive.ObjectID) (*models.WarehouseRevision, error) {
	return s.store.GetRevision(ctx, revisionID)
}

func (s *svc) RevisionHistory(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error) {
	return s.store.Revisions(ctx, warehouseID)
}

func (s *svc) ReviewQueue(ctx context.Context, page, limit int) ([]models.WarehouseRevision, int64, error) {
	return s.store.Queue(ctx, page, limit)
}

// --- media ---

func (s *svc) MediaUploadURL(ctx context.Context, actor domain.Actor, req catalogService.UploadRequest) (catalogService.UploadResponse, error) {
	return s.media.UploadURL(ctx, actor, req)
}

func (s *svc) ConfirmMedia(ctx context.Context, mediaID primitive.ObjectID) (*models.WarehouseMedia, error) {
	return s.media.Confirm(ctx, mediaID)
}

func (s *svc) MediaLink(ctx context.Context, mediaID primitive.ObjectID) (string, error) {
	return s.media.Link(ctx, mediaID)
}

func (s *svc) ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseMedia, error) {
	return s.store.ListMedia(ctx, warehouseID)
}

func (s *svc) MediaGC(ctx context.Context) error { return s.media.GC(ctx) }

// --- public ---

func (s *svc) PublicListing(ctx context.Context, slug string) (catalogService.LookupResult, error) {
	return s.public.Lookup(ctx, slug)
}

func (s *svc) PublicSlugs(ctx context.Context, page int, withCover bool) ([]dto.SlugItem, error) {
	return s.public.Slugs(ctx, page, withCover)
}
