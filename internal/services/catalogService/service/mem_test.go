package service

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/store"
)

// memStore is an in-memory Store with the same CAS semantics as Mongo.
// Docs are deep-copied through bson on the way in and out, so values come
// back in the shapes the driver produces (primitive.D, int32, …).
type memStore struct {
	mu         sync.Mutex
	warehouses map[primitive.ObjectID]models.Warehouse
	revisions  map[primitive.ObjectID]models.WarehouseRevision
	media      map[primitive.ObjectID]models.WarehouseMedia
	rents      map[primitive.ObjectID]models.WarehouseRent
	catalogV   int64
}

func newMemStore() *memStore {
	return &memStore{
		warehouses: map[primitive.ObjectID]models.Warehouse{},
		revisions:  map[primitive.ObjectID]models.WarehouseRevision{},
		media:      map[primitive.ObjectID]models.WarehouseMedia{},
		rents:      map[primitive.ObjectID]models.WarehouseRent{},
	}
}

func roundTrip[T any](v T) T {
	b, err := bson.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err := bson.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func (s *memStore) InsertWarehouse(_ context.Context, w *models.Warehouse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.warehouses {
		if x.ShortID == w.ShortID {
			return models.ErrDuplicateKey
		}
	}
	w.ID = primitive.NewObjectID()
	s.warehouses[w.ID] = roundTrip(*w)
	return nil
}

func (s *memStore) GetWarehouse(_ context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok {
		return nil, catalogService.ErrNotFound
	}
	w = roundTrip(w)
	return &w, nil
}

func (s *memStore) GetWarehouseByShortID(_ context.Context, sid string) (*models.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.warehouses {
		if w.ShortID == sid {
			w = roundTrip(w)
			return &w, nil
		}
	}
	return nil, catalogService.ErrNotFound
}

func (s *memStore) NextRevSeq(_ context.Context, id primitive.ObjectID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok {
		return 0, catalogService.ErrNotFound
	}
	w.RevSeq++
	s.warehouses[id] = w
	return w.RevSeq, nil
}

func (s *memStore) SetOpenRevision(_ context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.warehouses[id]
	w.OpenRevisionID, w.UpdatedAt = rev, at
	s.warehouses[id] = w
	return nil
}

func (s *memStore) SetDraftName(_ context.Context, id primitive.ObjectID, name, city string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.warehouses[id]
	if w.Status == models.WarehouseUnpublished {
		w.Name, w.City, w.UpdatedAt = name, city, at
		s.warehouses[id] = w
	}
	return nil
}

func (s *memStore) PublishLive(_ context.Context, id primitive.ObjectID, base int, p models.WarehousePublish) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok || w.LiveVersion != base {
		return false, nil
	}
	live := roundTrip(p.Live)
	w.Live, w.LiveVersion, w.Status, w.OpenRevisionID = &live, p.LiveVersion, models.WarehouseLive, nil
	w.Name, w.Country, w.PostalCode, w.City, w.Locality = p.Name, p.Country, p.PostalCode, p.City, p.Locality
	w.Loc, w.TotalSqm, w.Price, w.CoverKey = p.Loc, p.TotalSqm, p.Price, p.CoverKey
	w.Completeness, w.VerifiedRatio, w.Projection = p.Completeness, p.VerifiedRatio, p.Projection
	w.Slug, w.UpdatedAt = p.Slug, p.At
	if w.PublishedAt == nil {
		at := p.At
		w.PublishedAt = &at
	}
	if !slices.Contains(w.SlugHistory, p.Slug) {
		w.SlugHistory = append(w.SlugHistory, p.Slug)
	}
	w.ArchivedAt, w.ArchivedBy = nil, ""
	s.warehouses[id] = w
	return true, nil
}

func (s *memStore) Archive(_ context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.warehouses[id]
	if w.Status != models.WarehouseLive {
		return false, nil
	}
	w.Status, w.ArchivedAt, w.ArchivedBy, w.UpdatedAt = models.WarehouseArchived, &at, by, at
	s.warehouses[id] = w
	return true, nil
}

func (s *memStore) DeleteUnpublished(_ context.Context, id primitive.ObjectID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.warehouses[id]; !ok || w.Status != models.WarehouseUnpublished {
		return false, nil
	}
	delete(s.warehouses, id)
	return true, nil
}

func (s *memStore) ListWarehouses(_ context.Context, f models.WarehouseFilter, page, limit int) ([]models.Warehouse, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.Warehouse
	for _, w := range s.warehouses {
		if (f.Status != "" && w.Status != f.Status) || (f.Q != "" && !strings.Contains(strings.ToLower(w.Name), strings.ToLower(f.Q))) ||
			(f.City != "" && !strings.EqualFold(w.City, f.City)) || (f.NeedsInfo && w.NeedsInfoCount == 0) {
			continue
		}
		out = append(out, w)
	}
	return paginate(out, page, limit), int64(len(out)), nil
}

func paginate[T any](xs []T, page, limit int) []T {
	from := (page - 1) * limit
	if from >= len(xs) {
		return []T{}
	}
	return xs[from:min(len(xs), from+limit)]
}

func (s *memStore) ListLive(_ context.Context, page, limit int) ([]models.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.Warehouse
	for _, w := range s.warehouses {
		if w.Status == models.WarehouseLive {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.Hex() < out[j].ID.Hex() })
	return paginate(out, page, limit), nil
}

func (s *memStore) openExists(wid, except primitive.ObjectID) bool {
	for id, r := range s.revisions {
		if r.WarehouseID == wid && r.Open && id != except {
			return true
		}
	}
	return false
}

func (s *memStore) InsertRevision(_ context.Context, r *models.WarehouseRevision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Open && s.openExists(r.WarehouseID, primitive.NilObjectID) {
		return models.ErrDuplicateKey
	}
	r.ID = primitive.NewObjectID()
	s.revisions[r.ID] = roundTrip(*r)
	return nil
}

func (s *memStore) GetRevision(_ context.Context, id primitive.ObjectID) (*models.WarehouseRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.revisions[id]
	if !ok {
		return nil, catalogService.ErrNotFound
	}
	r = roundTrip(r)
	return &r, nil
}

func (s *memStore) ReplaceRevision(_ context.Context, r *models.WarehouseRevision, state models.RevisionState, rev int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.revisions[r.ID]
	if !ok || cur.State != state || cur.Rev != rev {
		return models.ErrVersionConflict
	}
	if r.Open && s.openExists(r.WarehouseID, r.ID) {
		return models.ErrDuplicateKey
	}
	s.revisions[r.ID] = roundTrip(*r)
	return nil
}

func (s *memStore) Revisions(_ context.Context, wid primitive.ObjectID) ([]models.WarehouseRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []models.WarehouseRevision{}
	for _, r := range s.revisions {
		if r.WarehouseID == wid {
			r.Content = models.ListingContent{}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (s *memStore) ApprovedRevisions(_ context.Context, wid primitive.ObjectID) ([]models.WarehouseRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []models.WarehouseRevision{}
	for _, r := range s.revisions {
		if r.WarehouseID == wid && r.State == models.RevApproved {
			out = append(out, roundTrip(r))
		}
	}
	return out, nil
}

func (s *memStore) Queue(_ context.Context, page, limit int) ([]models.WarehouseRevision, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.WarehouseRevision
	for _, r := range s.revisions {
		if r.State == models.RevInReview {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(*out[j].SubmittedAt) })
	return paginate(out, page, limit), int64(len(out)), nil
}

func (s *memStore) DeleteRevisions(_ context.Context, wid primitive.ObjectID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.revisions {
		if r.WarehouseID == wid {
			delete(s.revisions, id)
		}
	}
	return nil
}

func (s *memStore) InsertMedia(_ context.Context, m *models.WarehouseMedia) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID.IsZero() {
		m.ID = primitive.NewObjectID()
	}
	s.media[m.ID] = *m
	return nil
}

func (s *memStore) GetMedia(_ context.Context, id primitive.ObjectID) (*models.WarehouseMedia, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.media[id]
	if !ok {
		return nil, catalogService.ErrNotFound
	}
	return &m, nil
}

func (s *memStore) ReplaceMedia(_ context.Context, m *models.WarehouseMedia) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media[m.ID] = *m
	return nil
}

func (s *memStore) DeleteMedia(_ context.Context, id primitive.ObjectID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.media, id)
	return nil
}

func (s *memStore) ListMedia(_ context.Context, wid primitive.ObjectID) ([]models.WarehouseMedia, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []models.WarehouseMedia{}
	for _, m := range s.media {
		if m.WarehouseID == wid && m.Status != models.MediaOrphaned {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memStore) CountPhotos(_ context.Context, wid primitive.ObjectID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, m := range s.media {
		if m.WarehouseID == wid && m.Kind == models.MediaPhoto && m.Status != models.MediaOrphaned {
			n++
		}
	}
	return n, nil
}

func (s *memStore) MediaForGC(_ context.Context, pendingBefore, staleBefore time.Time) ([]models.WarehouseMedia, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.WarehouseMedia
	for _, m := range s.media {
		if (m.Status == models.MediaPending && m.CreatedAt.Before(pendingBefore)) ||
			(m.Status != models.MediaPending && m.CreatedAt.Before(staleBefore)) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memStore) ReferencedMedia(_ context.Context) (map[primitive.ObjectID]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[primitive.ObjectID]bool{}
	for _, r := range s.revisions {
		if r.State == models.RevDiscarded {
			continue
		}
		for _, id := range r.Content.MediaIDs() {
			out[id] = true
		}
	}
	return out, nil
}

func (s *memStore) UpsertRent(_ context.Context, r models.WarehouseRent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rents[r.WarehouseID] = r
	return nil
}

func (s *memStore) BumpCatalogVersion(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalogV++
	return s.catalogV, nil
}

var _ store.Store = (*memStore)(nil)
