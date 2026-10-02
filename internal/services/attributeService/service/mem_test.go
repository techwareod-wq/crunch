package service

import (
	"context"
	"errors"
	"github.com/atharva-ng/crunch/internal/models"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// memStore is an in-memory Store.
type memStore struct {
	*memWarehouses
	mu    sync.Mutex
	v     int64
	nodes map[string]models.AttributeNode
	inds  map[string]models.Industry
	// failReplace, when set, fails the next ReplaceNode once.
	failReplace error
}

func newMemStore() *memStore {
	return &memStore{memWarehouses: newMemWarehouses(), nodes: map[string]models.AttributeNode{}, inds: map[string]models.Industry{}}
}

func (s *memStore) Load(context.Context) (*domain.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := make([]models.AttributeNode, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n.Clone())
	}
	inds := make([]models.Industry, 0, len(s.inds))
	for _, i := range s.inds {
		inds = append(inds, i.Clone())
	}
	return domain.NewSnapshot(s.v, nodes, inds), nil
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

func (s *memStore) InsertNode(_ context.Context, n *models.AttributeNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[n.Key]; ok {
		return attributeService.ErrKeyExists
	}
	n.ID = primitive.NewObjectID()
	s.nodes[n.Key] = n.Clone()
	return nil
}

func (s *memStore) ReplaceNode(_ context.Context, n *models.AttributeNode, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failReplace != nil {
		err := s.failReplace
		s.failReplace = nil
		return err
	}
	cur, ok := s.nodes[n.Key]
	if !ok || cur.Version != expected {
		return attributeService.ErrVersionConflict
	}
	s.nodes[n.Key] = n.Clone()
	return nil
}

func (s *memStore) InsertIndustry(_ context.Context, ind *models.Industry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.inds[ind.Key]; ok {
		return attributeService.ErrKeyExists
	}
	ind.ID = primitive.NewObjectID()
	s.inds[ind.Key] = ind.Clone()
	return nil
}

func (s *memStore) ReplaceIndustry(_ context.Context, ind *models.Industry, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.inds[ind.Key]
	if !ok || cur.Version != expected {
		return attributeService.ErrVersionConflict
	}
	s.inds[ind.Key] = ind.Clone()
	return nil
}

func (s *memStore) DeleteIndustry(_ context.Context, key string, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.inds[key]
	if !ok || cur.Version != expected {
		return attributeService.ErrVersionConflict
	}
	delete(s.inds, key)
	return nil
}

// memWarehouses is an in-memory WarehouseStore with the same version guard
// as the Mongo BulkWrite filter.
type memWarehouses struct {
	mu     sync.Mutex
	status map[primitive.ObjectID]string
	docs   map[primitive.ObjectID]models.WarehouseEvalDoc
	proj   map[primitive.ObjectID]models.Projection
}

func newMemWarehouses() *memWarehouses {
	return &memWarehouses{status: map[primitive.ObjectID]string{}, docs: map[primitive.ObjectID]models.WarehouseEvalDoc{}, proj: map[primitive.ObjectID]models.Projection{}}
}

func (w *memWarehouses) add(status string, attrs models.Attributes) primitive.ObjectID {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := primitive.NewObjectID()
	d := models.WarehouseEvalDoc{ID: id}
	d.Live = &struct {
		Attributes models.Attributes `bson:"attributes"`
	}{Attributes: attrs}
	w.status[id], w.docs[id] = status, d
	return id
}

func (w *memWarehouses) StaleWarehouseIDs(_ context.Context, v int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []primitive.ObjectID
	for id, st := range w.status {
		if st != models.WarehouseLive && st != models.WarehouseArchived {
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

func (w *memWarehouses) WarehouseEvalDocs(_ context.Context, ids []primitive.ObjectID) ([]models.WarehouseEvalDoc, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []models.WarehouseEvalDoc
	for _, id := range ids {
		if d, ok := w.docs[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (w *memWarehouses) WriteProjections(_ context.Context, ps map[primitive.ObjectID]models.Projection) (int64, error) {
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
	svc        *svc
	warehouses *memWarehouses
	sent       []dispatched
	failNext   error
}

var actor = domain.Actor{UserID: "u1", Email: "ed@example.com"}

func newHarness() *harness {
	h := &harness{store: newMemStore(), cl: &changelog.Memory{}}
	h.warehouses = h.store.memWarehouses
	keyed := func(_ context.Context, pt pipeline.ProcessType, key string, payload any) error {
		if h.failNext != nil {
			err := h.failNext
			h.failNext = nil
			return err
		}
		h.sent = append(h.sent, dispatched{pt, key, payload})
		return nil
	}
	h.svc = newService(h.store, keyed, h.cl, 2, func() time.Time { return time.Unix(1000, 0) })
	h.cache = h.svc.cache
	return h
}

// booted runs the root bootstrap and clears the recorded side effects.
func (h *harness) booted() *harness {
	if err := h.svc.EnsureRoot(context.Background()); err != nil {
		panic(err)
	}
	h.sent = nil
	h.cl.Entries = nil
	return h
}

func (h *harness) node(key string) models.AttributeNode {
	snap, _ := h.store.Load(context.Background())
	n, ok := snap.Node(key)
	if !ok {
		panic("no node " + key)
	}
	return *n
}

var errBoom = errors.New("boom")
