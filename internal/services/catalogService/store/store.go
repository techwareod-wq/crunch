package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the catalog persistence. Mongo in production, memStore in tests.
// Every multi-doc flow is a sequence of single-doc CAS writes (no
// transactions, spec 00).
type Store interface {
	// warehouses
	InsertWarehouse(ctx context.Context, w *models.Warehouse) error // models.ErrDuplicateKey on shortId
	GetWarehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error)
	GetWarehouseByShortID(ctx context.Context, shortID string) (*models.Warehouse, error)
	NextRevSeq(ctx context.Context, id primitive.ObjectID) (int, error)
	SetOpenRevision(ctx context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error
	// SetDraftName updates the hoisted name/city of a never-published
	// warehouse (no-op once published).
	SetDraftName(ctx context.Context, id primitive.ObjectID, name, city string, at time.Time) error
	// PublishLive applies p when live_version == base; false = CAS miss.
	PublishLive(ctx context.Context, id primitive.ObjectID, base int, p models.WarehousePublish) (bool, error)
	// Archive moves live → archived; false = not live.
	Archive(ctx context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error)
	// DeleteUnpublished hard-deletes an unpublished warehouse; false = not
	// unpublished.
	DeleteUnpublished(ctx context.Context, id primitive.ObjectID) (bool, error)
	ListWarehouses(ctx context.Context, f models.WarehouseFilter, page, limit int) ([]models.Warehouse, int64, error)
	// ListLive pages live warehouses by _id (public slugs / sitemap).
	ListLive(ctx context.Context, page, limit int) ([]models.Warehouse, error)

	// revisions
	InsertRevision(ctx context.Context, r *models.WarehouseRevision) error // models.ErrDuplicateKey when one is already open
	GetRevision(ctx context.Context, id primitive.ObjectID) (*models.WarehouseRevision, error)
	// ReplaceRevision CAS-replaces r over the doc in (state, rev).
	ReplaceRevision(ctx context.Context, r *models.WarehouseRevision, state models.RevisionState, rev int) error
	// Revisions lists a warehouse's revisions, newest first, without content.
	Revisions(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error)
	// ApprovedRevisions lists a warehouse's approved revisions (with content).
	ApprovedRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error)
	// Queue pages in-review revisions, oldest submission first (no content).
	Queue(ctx context.Context, page, limit int) ([]models.WarehouseRevision, int64, error)
	DeleteRevisions(ctx context.Context, warehouseID primitive.ObjectID) error

	// media
	InsertMedia(ctx context.Context, m *models.WarehouseMedia) error
	GetMedia(ctx context.Context, id primitive.ObjectID) (*models.WarehouseMedia, error)
	ReplaceMedia(ctx context.Context, m *models.WarehouseMedia) error
	DeleteMedia(ctx context.Context, id primitive.ObjectID) error
	ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseMedia, error)
	// CountPhotos counts a warehouse's pending + ready photos.
	CountPhotos(ctx context.Context, warehouseID primitive.ObjectID) (int64, error)
	// MediaForGC lists pending media created before pendingBefore, and
	// ready/orphaned media created before staleBefore.
	MediaForGC(ctx context.Context, pendingBefore, staleBefore time.Time) ([]models.WarehouseMedia, error)
	// ReferencedMedia returns every media id a non-discarded revision
	// references (content.media or the agreement).
	ReferencedMedia(ctx context.Context) (map[primitive.ObjectID]bool, error)

	// rents
	UpsertRent(ctx context.Context, r models.WarehouseRent) error

	BumpCatalogVersion(ctx context.Context) (int64, error)
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) InsertWarehouse(ctx context.Context, w *models.Warehouse) error {
	return models.InsertWarehouse(ctx, w)
}

func (s *store) GetWarehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	return models.FindWarehouseByID(ctx, id)
}

func (s *store) GetWarehouseByShortID(ctx context.Context, shortID string) (*models.Warehouse, error) {
	return models.FindWarehouseByShortID(ctx, shortID)
}

func (s *store) NextRevSeq(ctx context.Context, id primitive.ObjectID) (int, error) {
	return models.NextWarehouseRevSeq(ctx, id)
}

func (s *store) SetOpenRevision(ctx context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error {
	return models.SetWarehouseOpenRevision(ctx, id, rev, at)
}

func (s *store) SetDraftName(ctx context.Context, id primitive.ObjectID, name, city string, at time.Time) error {
	return models.SetWarehouseDraftName(ctx, id, name, city, at)
}

func (s *store) PublishLive(ctx context.Context, id primitive.ObjectID, base int, p models.WarehousePublish) (bool, error) {
	return models.PublishWarehouse(ctx, id, base, p)
}

func (s *store) Archive(ctx context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error) {
	return models.ArchiveWarehouse(ctx, id, by, at)
}

func (s *store) DeleteUnpublished(ctx context.Context, id primitive.ObjectID) (bool, error) {
	return models.DeleteUnpublishedWarehouse(ctx, id)
}

func (s *store) ListWarehouses(ctx context.Context, f models.WarehouseFilter, page, limit int) ([]models.Warehouse, int64, error) {
	return models.ListWarehouses(ctx, f, page, limit)
}

func (s *store) ListLive(ctx context.Context, page, limit int) ([]models.Warehouse, error) {
	return models.ListLiveWarehouses(ctx, page, limit)
}

func (s *store) InsertRevision(ctx context.Context, r *models.WarehouseRevision) error {
	return models.InsertWarehouseRevision(ctx, r)
}

func (s *store) GetRevision(ctx context.Context, id primitive.ObjectID) (*models.WarehouseRevision, error) {
	return models.FindWarehouseRevisionByID(ctx, id)
}

func (s *store) ReplaceRevision(ctx context.Context, r *models.WarehouseRevision, state models.RevisionState, rev int) error {
	return models.ReplaceWarehouseRevision(ctx, r, state, rev)
}

func (s *store) Revisions(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error) {
	return models.ListWarehouseRevisions(ctx, warehouseID)
}

func (s *store) ApprovedRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error) {
	return models.ListApprovedWarehouseRevisions(ctx, warehouseID)
}

func (s *store) Queue(ctx context.Context, page, limit int) ([]models.WarehouseRevision, int64, error) {
	return models.ListReviewQueue(ctx, page, limit)
}

func (s *store) DeleteRevisions(ctx context.Context, warehouseID primitive.ObjectID) error {
	return models.DeleteWarehouseRevisions(ctx, warehouseID)
}

func (s *store) InsertMedia(ctx context.Context, m *models.WarehouseMedia) error {
	return models.InsertWarehouseMedia(ctx, m)
}

func (s *store) GetMedia(ctx context.Context, id primitive.ObjectID) (*models.WarehouseMedia, error) {
	return models.FindWarehouseMediaByID(ctx, id)
}

func (s *store) ReplaceMedia(ctx context.Context, m *models.WarehouseMedia) error {
	return models.ReplaceWarehouseMedia(ctx, m)
}

func (s *store) DeleteMedia(ctx context.Context, id primitive.ObjectID) error {
	return models.DeleteWarehouseMedia(ctx, id)
}

func (s *store) ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseMedia, error) {
	return models.ListWarehouseMedia(ctx, warehouseID)
}

func (s *store) CountPhotos(ctx context.Context, warehouseID primitive.ObjectID) (int64, error) {
	return models.CountWarehousePhotos(ctx, warehouseID)
}

func (s *store) MediaForGC(ctx context.Context, pendingBefore, staleBefore time.Time) ([]models.WarehouseMedia, error) {
	return models.ListWarehouseMediaForGC(ctx, pendingBefore, staleBefore)
}

func (s *store) ReferencedMedia(ctx context.Context) (map[primitive.ObjectID]bool, error) {
	return models.ReferencedMediaIDs(ctx)
}

func (s *store) UpsertRent(ctx context.Context, r models.WarehouseRent) error {
	return models.UpsertWarehouseRent(ctx, r)
}

func (s *store) BumpCatalogVersion(ctx context.Context) (int64, error) {
	return models.BumpCounter(ctx, models.CounterCatalogVersion)
}
