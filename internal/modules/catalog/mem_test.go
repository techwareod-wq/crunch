package catalog

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// memStore is an in-memory Store with the same CAS semantics as Mongo.
// Docs are deep-copied through bson on the way in and out, so values come
// back in the shapes the driver produces (primitive.D, int32, …).
type memStore struct {
	mu         sync.Mutex
	warehouses map[primitive.ObjectID]domain.Warehouse
	revisions  map[primitive.ObjectID]domain.Revision
	media      map[primitive.ObjectID]domain.Media
	rents      map[primitive.ObjectID]domain.WarehouseRent
	catalogV   int64
}

func newMemStore() *memStore {
	return &memStore{
		warehouses: map[primitive.ObjectID]domain.Warehouse{},
		revisions:  map[primitive.ObjectID]domain.Revision{},
		media:      map[primitive.ObjectID]domain.Media{},
		rents:      map[primitive.ObjectID]domain.WarehouseRent{},
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

func (s *memStore) InsertWarehouse(_ context.Context, w *domain.Warehouse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.warehouses {
		if x.ShortID == w.ShortID {
			return errDuplicate
		}
	}
	w.ID = primitive.NewObjectID()
	s.warehouses[w.ID] = roundTrip(*w)
	return nil
}

func (s *memStore) GetWarehouse(_ context.Context, id primitive.ObjectID) (*domain.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok {
		return nil, errNotFound
	}
	w = roundTrip(w)
	return &w, nil
}

func (s *memStore) GetWarehouseByShortID(_ context.Context, sid string) (*domain.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.warehouses {
		if w.ShortID == sid {
			w = roundTrip(w)
			return &w, nil
		}
	}
	return nil, errNotFound
}

func (s *memStore) NextRevSeq(_ context.Context, id primitive.ObjectID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok {
		return 0, errNotFound
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
	if w.Status == domain.WarehouseUnpublished {
		w.Name, w.City, w.UpdatedAt = name, city, at
		s.warehouses[id] = w
	}
	return nil
}

func (s *memStore) PublishLive(_ context.Context, id primitive.ObjectID, base int, p PublishPatch) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.warehouses[id]
	if !ok || w.LiveVersion != base {
		return false, nil
	}
	live := roundTrip(p.Live)
	w.Live, w.LiveVersion, w.Status, w.OpenRevisionID = &live, p.LiveVersion, domain.WarehouseLive, nil
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
	if w.Status != domain.WarehouseLive {
		return false, nil
	}
	w.Status, w.ArchivedAt, w.ArchivedBy, w.UpdatedAt = domain.WarehouseArchived, &at, by, at
	s.warehouses[id] = w
	return true, nil
}

func (s *memStore) DeleteUnpublished(_ context.Context, id primitive.ObjectID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.warehouses[id]; !ok || w.Status != domain.WarehouseUnpublished {
		return false, nil
	}
	delete(s.warehouses, id)
	return true, nil
}

func (s *memStore) ListWarehouses(_ context.Context, f WarehouseFilter, page, limit int) ([]domain.Warehouse, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Warehouse
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

func (s *memStore) ListLive(_ context.Context, page, limit int) ([]domain.Warehouse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Warehouse
	for _, w := range s.warehouses {
		if w.Status == domain.WarehouseLive {
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

func (s *memStore) InsertRevision(_ context.Context, r *domain.Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Open && s.openExists(r.WarehouseID, primitive.NilObjectID) {
		return errDuplicate
	}
	r.ID = primitive.NewObjectID()
	s.revisions[r.ID] = roundTrip(*r)
	return nil
}

func (s *memStore) GetRevision(_ context.Context, id primitive.ObjectID) (*domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.revisions[id]
	if !ok {
		return nil, errNotFound
	}
	r = roundTrip(r)
	return &r, nil
}

func (s *memStore) ReplaceRevision(_ context.Context, r *domain.Revision, state domain.RevisionState, rev int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.revisions[r.ID]
	if !ok || cur.State != state || cur.Rev != rev {
		return errCAS
	}
	if r.Open && s.openExists(r.WarehouseID, r.ID) {
		return errDuplicate
	}
	s.revisions[r.ID] = roundTrip(*r)
	return nil
}

func (s *memStore) Revisions(_ context.Context, wid primitive.ObjectID) ([]domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Revision{}
	for _, r := range s.revisions {
		if r.WarehouseID == wid {
			r.Content = domain.Content{}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (s *memStore) ApprovedRevisions(_ context.Context, wid primitive.ObjectID) ([]domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Revision{}
	for _, r := range s.revisions {
		if r.WarehouseID == wid && r.State == domain.RevApproved {
			out = append(out, roundTrip(r))
		}
	}
	return out, nil
}

func (s *memStore) Queue(_ context.Context, page, limit int) ([]domain.Revision, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Revision
	for _, r := range s.revisions {
		if r.State == domain.RevInReview {
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

func (s *memStore) InsertMedia(_ context.Context, m *domain.Media) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID.IsZero() {
		m.ID = primitive.NewObjectID()
	}
	s.media[m.ID] = *m
	return nil
}

func (s *memStore) GetMedia(_ context.Context, id primitive.ObjectID) (*domain.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.media[id]
	if !ok {
		return nil, errNotFound
	}
	return &m, nil
}

func (s *memStore) ReplaceMedia(_ context.Context, m *domain.Media) error {
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

func (s *memStore) ListMedia(_ context.Context, wid primitive.ObjectID) ([]domain.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Media{}
	for _, m := range s.media {
		if m.WarehouseID == wid && m.Status != domain.MediaOrphaned {
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
		if m.WarehouseID == wid && m.Kind == domain.MediaPhoto && m.Status != domain.MediaOrphaned {
			n++
		}
	}
	return n, nil
}

func (s *memStore) MediaForGC(_ context.Context, pendingBefore, staleBefore time.Time) ([]domain.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Media
	for _, m := range s.media {
		if (m.Status == domain.MediaPending && m.CreatedAt.Before(pendingBefore)) ||
			(m.Status != domain.MediaPending && m.CreatedAt.Before(staleBefore)) {
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
		if r.State == domain.RevDiscarded {
			continue
		}
		for _, id := range referencedBy(r.Content) {
			out[id] = true
		}
	}
	return out, nil
}

func (s *memStore) UpsertRent(_ context.Context, r domain.WarehouseRent) error {
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

var _ Store = (*memStore)(nil)
