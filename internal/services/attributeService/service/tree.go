package service

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// bootstrapRetries bounds the root bootstrap's CAS retries (another instance
// booting at the same time).
const bootstrapRetries = 3

func (s *svc) afterWrite(ctx context.Context, changed []string) attributeService.WriteResult {
	res := attributeService.WriteResult{Changed: changed}
	if len(changed) == 0 {
		res.Changed = []string{}
		res.RulesVersion = s.cache.Snapshot().Version
		return res
	}
	ctx = context.WithoutCancel(ctx)
	v, err := s.store.BumpRulesVersion(ctx)
	if err != nil {
		log.Error("attributes: rulesVersion bump failed — recompute deferred to the next write or safety net", "error", err)
	}
	res.RulesVersion = v
	if err := s.cache.Reload(ctx); err != nil {
		log.Error("attributes: cache reload after write failed", "error", err)
	}
	if v > 0 {
		if err := s.dispatchRecompute(ctx, v); err != nil {
			log.Error("attributes: recompute dispatch failed", "rules_version", v, "error", err)
		}
	}
	return res
}

func (s *svc) record(ctx context.Context, actor domain.Actor, entity domain.ChangeEntity, id string, action domain.ChangeAction, before, after any, meta map[string]any) {
	s.log.Record(ctx, domain.ChangeEntry{Entity: entity, EntityID: id, Action: action, Actor: actor, Before: before, After: after, Meta: meta})
}

func (s *svc) stamp(n *models.AttributeNode, actor domain.Actor) {
	n.UpdatedBy = actor.Email
	n.UpdatedAt = s.now().UTC()
}

// replaceNode bumps the version and CAS-writes upd over old.
func (s *svc) replaceNode(ctx context.Context, actor domain.Actor, old, upd *models.AttributeNode, action domain.ChangeAction, meta map[string]any) error {
	upd.ID = old.ID
	upd.Version = old.Version + 1
	s.stamp(upd, actor)
	if err := s.store.ReplaceNode(ctx, upd, old.Version); err != nil {
		return err
	}
	s.record(ctx, actor, domain.EntityAttributeDef, upd.Key, action, old, upd, meta)
	return nil
}

// loadNode reads a fresh snapshot and the node, checking expectedVersion.
func (s *svc) loadNode(ctx context.Context, key string, expected int) (*domain.Snapshot, *models.AttributeNode, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	n, ok := snap.Node(key)
	if !ok {
		return nil, nil, attributeService.ErrNotFound
	}
	if n.Version != expected {
		return nil, nil, attributeService.ErrVersionConflict
	}
	return snap, n, nil
}

func nextNodeOrder(snap *domain.Snapshot, parent string) int {
	o := 0
	for _, k := range snap.Children(parent) {
		n, _ := snap.Node(k)
		o = max(o, n.Order)
	}
	return o + 1
}

// --- root bootstrap (D-142) ---

// EnsureRoot creates the Warehouse root and any missing system field.
// Idempotent: existing fields are never touched, so an admin's rename,
// description or order survives every boot.
func (s *svc) EnsureRoot(ctx context.Context) error {
	want := domain.RootNode()
	for attempt := 0; attempt < bootstrapRetries; attempt++ {
		snap, err := s.store.Load(ctx)
		if err != nil {
			return err
		}
		old, ok := snap.Node(domain.RootKey)
		if !ok {
			n := want
			n.Version = 1
			s.stamp(&n, domain.SystemActor)
			err := s.store.InsertNode(ctx, &n)
			if errors.Is(err, attributeService.ErrKeyExists) {
				continue // another instance won the race; re-check its fields
			}
			if err != nil {
				return err
			}
			s.record(ctx, domain.SystemActor, domain.EntityAttributeDef, n.Key, domain.ActionCreate, nil, n, map[string]any{"via": "bootstrap"})
			s.afterWrite(ctx, []string{n.Key})
			log.Info("attributes: created the Warehouse root node")
			return nil
		}
		upd := old.Clone()
		var added []string
		for _, f := range want.Fields {
			if _, has := upd.Field(f.Key); !has {
				f.Order = max(f.Order, len(upd.Fields)+1)
				upd.Fields = append(upd.Fields, f)
				added = append(added, f.Key)
			}
		}
		if len(added) == 0 && upd.System {
			return nil
		}
		upd.System = true
		err = s.replaceNode(ctx, domain.SystemActor, old, &upd, domain.ActionUpdate, map[string]any{"via": "bootstrap", "addedFields": added})
		if errors.Is(err, attributeService.ErrVersionConflict) {
			continue
		}
		if err != nil {
			return err
		}
		s.afterWrite(ctx, []string{upd.Key})
		log.Info("attributes: added missing root fields", "fields", added)
		return nil
	}
	return fmt.Errorf("root bootstrap: still conflicting after %d attempts", bootstrapRetries)
}

// --- nodes ---

// CreateNode adds a node (optionally with fields). Order ≤ 0 appends after
// the last sibling. def is required (D-126).
func (s *svc) CreateNode(ctx context.Context, actor domain.Actor, n models.AttributeNode, def attributeService.NewNodeDefault) (models.AttributeNode, attributeService.WriteResult, error) {
	if def != attributeService.DefaultUnknown && def != attributeService.DefaultNo {
		return n, attributeService.WriteResult{}, attributeService.Invalidf("default must be %q or %q (D-126)", attributeService.DefaultUnknown, attributeService.DefaultNo)
	}
	snap, err := s.store.Load(ctx)
	if err != nil {
		return n, attributeService.WriteResult{}, err
	}
	if _, exists := snap.Node(n.Key); exists {
		return n, attributeService.WriteResult{}, attributeService.ErrKeyExists
	}
	if n.Key == domain.RootKey {
		return n, attributeService.WriteResult{}, attributeService.Invalidf("%q is the system root", domain.RootKey)
	}
	n.System = false
	for i := range n.Fields {
		n.Fields[i].Locked = false
		if n.Fields[i].Order <= 0 {
			n.Fields[i].Order = i + 1
		}
	}
	if n.Order <= 0 {
		n.Order = nextNodeOrder(snap, n.ParentKey)
	}
	if n, err = domain.ValidateNode(snap, n); err != nil {
		return n, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	if err := s.checkKeyFree(ctx, n.Key, "node "+n.Key); err != nil {
		return n, attributeService.WriteResult{}, err
	}
	n.ID = primitive.NilObjectID
	n.Version = 1
	s.stamp(&n, actor)
	if err := s.store.InsertNode(ctx, &n); err != nil {
		return n, attributeService.WriteResult{}, err
	}
	meta := map[string]any{"default": string(def)}
	var markErr error
	if def == attributeService.DefaultUnknown {
		meta["batchId"], meta["marked"], markErr = s.markUnknown(ctx, actor, n)
	}
	s.record(ctx, actor, domain.EntityAttributeDef, n.Key, domain.ActionCreate, nil, n, meta)
	// The recompute runs after the markers so projections pick them up.
	res := s.afterWrite(ctx, []string{n.Key})
	if markErr != nil {
		return n, res, fmt.Errorf("node %q created, but its unknown markers are incomplete: %w", n.Key, markErr)
	}
	return n, res, nil
}

// markUnknown writes the new node's {status: unknown} markers directly onto
// live copies and open revisions whose parent is yes, without review
// (D-128), and logs one change_log row per warehouse under one batchId.
func (s *svc) markUnknown(ctx context.Context, actor domain.Actor, n models.AttributeNode) (string, int, error) {
	batchID := primitive.NewObjectID().Hex()
	ids, err := s.store.MarkNodeUnknown(context.WithoutCancel(ctx), n.Key, n.ParentKey, s.now().UTC())
	for _, id := range ids {
		s.record(ctx, actor, domain.EntityWarehouse, id.Hex(), domain.ActionUpdate, nil,
			map[string]any{"attributes." + n.Key: models.NodeState{Status: domain.StatusUnknown}},
			map[string]any{"op": "new_node_unknown", "node": n.Key, "batchId": batchID})
	}
	if len(ids) > 0 {
		log.Info("attributes: unknown markers written", "node", n.Key, "warehouses", len(ids), "batch", batchID)
	}
	return batchID, len(ids), err
}

// UpdateNode applies p with an expectedVersion CAS.
func (s *svc) UpdateNode(ctx context.Context, actor domain.Actor, p attributeService.NodePatch) (models.AttributeNode, attributeService.WriteResult, error) {
	snap, old, err := s.loadNode(ctx, p.Key, p.ExpectedVersion)
	if err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, err
	}
	upd := old.Clone()
	if p.Name != nil {
		upd.Name = *p.Name
	}
	if p.Description != nil {
		upd.Description = *p.Description
	}
	if p.Public != nil {
		upd.Public = *p.Public
	}
	if p.Filterable != nil {
		upd.Filterable = *p.Filterable
	}
	if p.FilterRow != nil {
		upd.FilterRow = *p.FilterRow
	}
	if p.FilterPos != nil {
		upd.FilterPos = *p.FilterPos
	}
	if p.Synonyms != nil {
		upd.Synonyms = slices.Clone(*p.Synonyms)
	}
	if upd, err = domain.ValidateNode(snap, upd); err != nil {
		return upd, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionUpdate, nil); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{upd.Key}), nil
}

// MoveNode re-parents a node; its children follow. order ≤ 0 appends after
// the new siblings. The root can't move.
func (s *svc) MoveNode(ctx context.Context, actor domain.Actor, key string, expected int, newParent string, order int) (models.AttributeNode, attributeService.WriteResult, error) {
	snap, old, err := s.loadNode(ctx, key, expected)
	if err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, err
	}
	if old.System {
		return models.AttributeNode{}, attributeService.WriteResult{}, attributeService.Invalidf("the root can't move")
	}
	upd := old.Clone()
	upd.ParentKey = newParent
	if order <= 0 {
		order = nextNodeOrder(snap, newParent)
	}
	upd.Order = order
	if upd, err = domain.ValidateNode(snap, upd); err != nil {
		return upd, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	meta := map[string]any{"fromParent": old.ParentKey, "toParent": newParent}
	if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionMove, meta); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{key}), nil
}

// ReorderNodes sets the sibling order under parentKey; keys must be exactly
// the current children.
func (s *svc) ReorderNodes(ctx context.Context, actor domain.Actor, parentKey string, keys []string) (attributeService.WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return attributeService.WriteResult{}, err
	}
	if err := sameSet(snap.Children(parentKey), keys); err != nil {
		return attributeService.WriteResult{}, attributeService.Invalidf("keys must list exactly the children of %q: %v", parentKey, err)
	}
	var changed []string
	for i, k := range keys {
		old, _ := snap.Node(k)
		if old.Order == i+1 {
			continue
		}
		upd := old.Clone()
		upd.Order = i + 1
		if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionUpdate, map[string]any{"op": "reorder"}); err != nil {
			s.afterWrite(ctx, changed)
			return attributeService.WriteResult{Changed: changed}, err
		}
		changed = append(changed, k)
	}
	return s.afterWrite(ctx, changed), nil
}

// --- fields ---

// CreateField adds a field to a node. Order ≤ 0 appends. Admin-added fields
// are never locked.
func (s *svc) CreateField(ctx context.Context, actor domain.Actor, nodeKey string, expected int, f models.AttributeField) (models.AttributeNode, attributeService.WriteResult, error) {
	snap, old, err := s.loadNode(ctx, nodeKey, expected)
	if err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, err
	}
	if _, exists := old.Field(f.Key); exists {
		return models.AttributeNode{}, attributeService.WriteResult{}, attributeService.ErrKeyExists
	}
	upd := old.Clone()
	f.Locked = false
	if f.Order <= 0 {
		for _, x := range upd.Fields {
			f.Order = max(f.Order, x.Order)
		}
		f.Order++
	}
	upd.Fields = append(upd.Fields, f)
	if upd, err = domain.ValidateNode(snap, upd); err != nil {
		return upd, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	if err := s.checkKeyFree(ctx, nodeKey+".fields."+f.Key, "field "+nodeKey+"."+f.Key); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	meta := map[string]any{"op": "field_create", "field": f.Key}
	if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionUpdate, meta); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{nodeKey}), nil
}

// UpdateField replaces one field. Key, type (D-130), locked and existing
// options are immutable; a locked field changes name, description and order
// only (D-140). Making a field required touches no warehouse (D-127).
func (s *svc) UpdateField(ctx context.Context, actor domain.Actor, nodeKey string, expected int, f models.AttributeField) (models.AttributeNode, attributeService.WriteResult, error) {
	snap, old, err := s.loadNode(ctx, nodeKey, expected)
	if err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, err
	}
	cur, ok := old.Field(f.Key)
	if !ok {
		return models.AttributeNode{}, attributeService.WriteResult{}, attributeService.ErrNotFound
	}
	f.Locked = cur.Locked
	if f.Order <= 0 {
		f.Order = cur.Order
	}
	if err := domain.CheckFieldUpdate(*cur, f); err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	upd := old.Clone()
	for i := range upd.Fields {
		if upd.Fields[i].Key == f.Key {
			upd.Fields[i] = f
		}
	}
	if upd, err = domain.ValidateNode(snap, upd); err != nil {
		return upd, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	meta := map[string]any{"op": "field_update", "field": f.Key}
	if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionUpdate, meta); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{nodeKey}), nil
}

// ReorderFields sets a node's field order; keys must be exactly its fields.
func (s *svc) ReorderFields(ctx context.Context, actor domain.Actor, nodeKey string, expected int, keys []string) (models.AttributeNode, attributeService.WriteResult, error) {
	_, old, err := s.loadNode(ctx, nodeKey, expected)
	if err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, err
	}
	cur := make([]string, len(old.Fields))
	for i, f := range old.Fields {
		cur[i] = f.Key
	}
	if err := sameSet(cur, keys); err != nil {
		return models.AttributeNode{}, attributeService.WriteResult{}, attributeService.Invalidf("keys must list exactly the fields of %q: %v", nodeKey, err)
	}
	upd := old.Clone()
	for i := range upd.Fields {
		upd.Fields[i].Order = slices.Index(keys, upd.Fields[i].Key) + 1
	}
	slices.SortFunc(upd.Fields, func(a, b models.AttributeField) int { return a.Order - b.Order })
	meta := map[string]any{"op": "field_reorder"}
	if err := s.replaceNode(ctx, actor, old, &upd, domain.ActionUpdate, meta); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{nodeKey}), nil
}

// sameSet reports whether keys is a permutation of cur.
func sameSet(cur, keys []string) error {
	if len(cur) != len(keys) {
		return fmt.Errorf("expected %d keys, got %d", len(cur), len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !slices.Contains(cur, k) || seen[k] {
			return fmt.Errorf("%q is unknown or repeated", k)
		}
		seen[k] = true
	}
	return nil
}

// --- industries ---

func (s *svc) loadIndustry(ctx context.Context, key string, expected int) (*domain.Snapshot, *models.Industry, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	ind, ok := snap.Industry(key)
	if !ok {
		return nil, nil, attributeService.ErrNotFound
	}
	if ind.Version != expected {
		return nil, nil, attributeService.ErrVersionConflict
	}
	return snap, ind, nil
}

// CreateIndustry adds an industry. Order ≤ 0 appends.
func (s *svc) CreateIndustry(ctx context.Context, actor domain.Actor, ind models.Industry) (models.Industry, attributeService.WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return ind, attributeService.WriteResult{}, err
	}
	if _, exists := snap.Industry(ind.Key); exists {
		return ind, attributeService.WriteResult{}, attributeService.ErrKeyExists
	}
	if ind.Order <= 0 {
		ind.Order = 1
		for _, x := range snap.Industries {
			ind.Order = max(ind.Order, x.Order+1)
		}
	}
	if ind, err = domain.ValidateIndustry(snap, ind); err != nil {
		return ind, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	ind.ID = primitive.NilObjectID
	ind.Version = 1
	ind.UpdatedBy = actor.Email
	ind.UpdatedAt = s.now().UTC()
	if err := s.store.InsertIndustry(ctx, &ind); err != nil {
		return ind, attributeService.WriteResult{}, err
	}
	s.record(ctx, actor, domain.EntityIndustry, ind.Key, domain.ActionCreate, nil, ind, nil)
	return ind, s.afterWrite(ctx, []string{ind.Key}), nil
}

// UpdateIndustry applies p with an expectedVersion CAS.
func (s *svc) UpdateIndustry(ctx context.Context, actor domain.Actor, p attributeService.IndustryPatch) (models.Industry, attributeService.WriteResult, error) {
	snap, old, err := s.loadIndustry(ctx, p.Key, p.ExpectedVersion)
	if err != nil {
		return models.Industry{}, attributeService.WriteResult{}, err
	}
	upd := old.Clone()
	if p.Name != nil {
		upd.Name = *p.Name
	}
	if p.Order != nil {
		upd.Order = *p.Order
	}
	if p.Required != nil {
		upd.Required = slices.Clone(*p.Required)
	}
	if p.Preferred != nil {
		upd.Preferred = slices.Clone(*p.Preferred)
	}
	if upd, err = domain.ValidateIndustry(snap, upd); err != nil {
		return upd, attributeService.WriteResult{}, attributeService.Invalid(err)
	}
	upd.ID = old.ID
	upd.Version = old.Version + 1
	upd.UpdatedBy = actor.Email
	upd.UpdatedAt = s.now().UTC()
	if err := s.store.ReplaceIndustry(ctx, &upd, old.Version); err != nil {
		return upd, attributeService.WriteResult{}, err
	}
	s.record(ctx, actor, domain.EntityIndustry, upd.Key, domain.ActionUpdate, old, upd, nil)
	return upd, s.afterWrite(ctx, []string{upd.Key}), nil
}

// DeleteIndustry hard-deletes an industry (superuser, D-122). The next
// recompute drops its verdicts from every projection.
func (s *svc) DeleteIndustry(ctx context.Context, actor domain.Actor, key string, expected int) (attributeService.WriteResult, error) {
	_, old, err := s.loadIndustry(ctx, key, expected)
	if err != nil {
		return attributeService.WriteResult{}, err
	}
	if err := s.store.DeleteIndustry(ctx, key, expected); err != nil {
		return attributeService.WriteResult{}, err
	}
	s.record(ctx, actor, domain.EntityIndustry, key, domain.ActionDelete, old, nil, nil)
	return s.afterWrite(ctx, []string{key}), nil
}
