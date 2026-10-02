package attributes

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// memStore is an in-memory Store.
type memStore struct {
	mu   sync.Mutex
	v    int64
	defs map[string]domain.AttrDef
	inds map[string]domain.Industry
}

func newMemStore() *memStore {
	return &memStore{defs: map[string]domain.AttrDef{}, inds: map[string]domain.Industry{}}
}

func (s *memStore) Load(context.Context) (*domain.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defs := make([]domain.AttrDef, 0, len(s.defs))
	for _, d := range s.defs {
		defs = append(defs, cloneDef(d))
	}
	inds := make([]domain.Industry, 0, len(s.inds))
	for _, i := range s.inds {
		inds = append(inds, i)
	}
	return domain.NewSnapshot(s.v, defs, inds), nil
}

func (s *memStore) RulesVersion(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v, nil
}

func (s *memStore) BumpRulesVersion(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v++
	return s.v, nil
}

func (s *memStore) InsertDef(_ context.Context, d *domain.AttrDef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[d.Key]; ok {
		return errKeyExists
	}
	d.ID = primitive.NewObjectID()
	s.defs[d.Key] = cloneDef(*d)
	return nil
}

func (s *memStore) ReplaceDef(_ context.Context, d *domain.AttrDef, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.defs[d.Key]
	if !ok || cur.Version != expected {
		return errVersionConflict
	}
	s.defs[d.Key] = cloneDef(*d)
	return nil
}

func (s *memStore) InsertIndustry(_ context.Context, ind *domain.Industry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.inds[ind.Key]; ok {
		return errKeyExists
	}
	ind.ID = primitive.NewObjectID()
	s.inds[ind.Key] = *ind
	return nil
}

func (s *memStore) ReplaceIndustry(_ context.Context, ind *domain.Industry, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.inds[ind.Key]
	if !ok || cur.Version != expected {
		return errVersionConflict
	}
	s.inds[ind.Key] = *ind
	return nil
}

// memWarehouses is an in-memory WarehouseStore with the same version guard
// as the Mongo BulkWrite filter.
type memWarehouses struct {
	mu     sync.Mutex
	status map[primitive.ObjectID]string
	docs   map[primitive.ObjectID]domain.LiveEvalDoc
	proj   map[primitive.ObjectID]domain.Projection
}

func newMemWarehouses() *memWarehouses {
	return &memWarehouses{status: map[primitive.ObjectID]string{}, docs: map[primitive.ObjectID]domain.LiveEvalDoc{}, proj: map[primitive.ObjectID]domain.Projection{}}
}

func (w *memWarehouses) add(status string, attrs map[string]domain.Answer) primitive.ObjectID {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := primitive.NewObjectID()
	d := domain.LiveEvalDoc{ID: id}
	d.Live = &struct {
		Attributes map[string]domain.Answer `bson:"attributes"`
		TotalArea  *domain.Area             `bson:"total_area"`
	}{Attributes: attrs, TotalArea: &domain.Area{Sqm: 1000}}
	w.status[id], w.docs[id] = status, d
	return id
}

func (w *memWarehouses) StaleIDs(_ context.Context, v int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []primitive.ObjectID
	for id, st := range w.status {
		if st != domain.WarehouseLive && st != domain.WarehouseArchived {
			continue
		}
		if p, ok := w.proj[id]; ok && p.FitRulesVersion >= v {
			continue
		}
		if !after.IsZero() && id.Hex() <= after.Hex() {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Hex() < ids[j].Hex() })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (w *memWarehouses) LoadEvalDocs(_ context.Context, ids []primitive.ObjectID) ([]domain.LiveEvalDoc, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []domain.LiveEvalDoc
	for _, id := range ids {
		if d, ok := w.docs[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (w *memWarehouses) WriteProjections(_ context.Context, ps map[primitive.ObjectID]domain.Projection) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var n int64
	for id, p := range ps {
		if cur, ok := w.proj[id]; ok && cur.FitRulesVersion >= p.FitRulesVersion {
			continue
		}
		w.proj[id] = p
		n++
	}
	return n, nil
}

type dispatched struct {
	pt      pipeline.ProcessType
	key     string
	payload any
}

type harness struct {
	store      *memStore
	cache      *Cache
	cl         *changelog.Memory
	svc        *Service
	rc         *recomputer
	warehouses *memWarehouses
	sent       []dispatched
	failNext   error
}

var actor = domain.Actor{UserID: "u1", Email: "ed@example.com"}

func newHarness() *harness {
	h := &harness{store: newMemStore(), cl: &changelog.Memory{}, warehouses: newMemWarehouses()}
	h.cache = NewCache(h.store.Load)
	keyed := func(_ context.Context, pt pipeline.ProcessType, key string, payload any) error {
		if h.failNext != nil {
			err := h.failNext
			h.failNext = nil
			return err
		}
		h.sent = append(h.sent, dispatched{pt, key, payload})
		return nil
	}
	h.svc = &Service{
		store: h.store, cache: h.cache, log: h.cl, now: func() time.Time { return time.Unix(1000, 0) },
		dispatch: func(ctx context.Context, v int64) error {
			return keyed(ctx, ProcessRecomputeAll, allKey(v, runRules), RecomputeAllPayload{RulesVersion: v, Run: runRules})
		},
	}
	h.rc = &recomputer{
		rules: h.cache, warehouses: h.warehouses, rulesVersion: h.store.RulesVersion,
		dispatchKeyed: keyed, batchSize: func() int { return 2 }, now: time.Now,
	}
	return h
}

func (h *harness) seed() {
	if _, err := h.svc.Seed(context.Background(), actor, true); err != nil {
		panic(err)
	}
	h.sent = nil
	h.cl.Entries = nil
}

var errBoom = errors.New("boom")
