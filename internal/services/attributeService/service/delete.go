package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// DeleteDefinition previews (Confirm=false) or performs a hard delete
// (D-129/D-130/D-138). Order: delete the definition (leaf-first for a
// subtree, so a partial failure never orphans a node) → change_log → bump
// rulesVersion + recompute → dispatch attributes.strip. Until the strip
// finishes the stale values are ignored: the evaluator only reads keys the
// snapshot knows.
func (s *svc) DeleteDefinition(ctx context.Context, actor domain.Actor, t attributeService.DeleteTarget) (attributeService.DeletePreview, attributeService.WriteResult, error) {
	pv := attributeService.DeletePreview{Nodes: []string{}, BlockedBy: []string{}}
	none := attributeService.WriteResult{Changed: []string{}}
	snap, n, err := s.loadNode(ctx, t.Node, t.ExpectedVersion)
	if err != nil {
		return pv, none, err
	}

	var paths []string // strip targets, relative to `attributes`
	switch {
	case t.Option != "":
		f, ok := n.Field(t.Field)
		if !ok || !f.HasOption(t.Option) {
			return pv, none, attributeService.ErrNotFound
		}
		pv.BlockedBy = append(pv.BlockedBy, industryRefs(snap, n.Key, f.Key, t.Option)...)
		if pv.Warehouses, err = s.store.CountUsingOption(ctx, n.Key, f.Key, t.Option); err != nil {
			return pv, none, err
		}
		if pv.Warehouses > 0 {
			pv.BlockedBy = append(pv.BlockedBy, fmt.Sprintf("used by %d warehouses", pv.Warehouses))
		}
	case t.Field != "":
		f, ok := n.Field(t.Field)
		if !ok {
			return pv, none, attributeService.ErrNotFound
		}
		if f.Locked {
			return pv, none, attributeService.Invalidf("%s.%s is a locked system field and can't be deleted (D-140)", n.Key, f.Key)
		}
		path := n.Key + "." + f.Key
		pv.BlockedBy = append(pv.BlockedBy, industryRefs(snap, n.Key, f.Key, "")...)
		pv.BlockedBy = append(pv.BlockedBy, ratioRefs(snap, func(p string) bool { return p == path })...)
		paths = []string{n.Key + ".fields." + f.Key}
	default:
		if n.System {
			return pv, none, attributeService.Invalidf("the root node can't be deleted")
		}
		pv.Nodes = append([]string{n.Key}, snap.Descendants(n.Key)...)
		for _, k := range pv.Nodes {
			pv.BlockedBy = append(pv.BlockedBy, industryRefs(snap, k, "", "")...)
		}
		inSet := func(p string) bool { return slices.Contains(pv.Nodes, nodeOf(p)) }
		pv.BlockedBy = append(pv.BlockedBy, ratioRefs(snap, inSet)...)
		paths = pv.Nodes
	}
	if len(paths) > 0 {
		if pv.Warehouses, err = s.store.CountHolding(ctx, paths); err != nil {
			return pv, none, err
		}
	}
	pv.BlockedBy = dedupe(pv.BlockedBy)
	if !t.Confirm {
		none.RulesVersion = snap.Version
		return pv, none, nil
	}
	if len(pv.BlockedBy) > 0 {
		return pv, none, attributeService.Invalidf("delete blocked by: %s", strings.Join(pv.BlockedBy, ", "))
	}

	pv.BatchID = primitive.NewObjectID().Hex()
	meta := map[string]any{"batchId": pv.BatchID, "warehouses": pv.Warehouses}
	target := n.Key
	switch {
	case t.Option != "":
		upd := n.Clone()
		for i := range upd.Fields {
			if upd.Fields[i].Key == t.Field {
				upd.Fields[i].Options = slices.DeleteFunc(upd.Fields[i].Options, func(o models.FieldOption) bool { return o.Key == t.Option })
			}
		}
		meta["op"], meta["field"], meta["option"] = "option_delete", t.Field, t.Option
		if err := s.replaceNode(ctx, actor, n, &upd, domain.ActionDelete, meta); err != nil {
			return pv, none, err
		}
		return pv, s.afterWrite(ctx, []string{n.Key}), nil
	case t.Field != "":
		upd := n.Clone()
		upd.Fields = slices.DeleteFunc(upd.Fields, func(f models.AttributeField) bool { return f.Key == t.Field })
		meta["op"], meta["field"] = "field_delete", t.Field
		if err := s.replaceNode(ctx, actor, n, &upd, domain.ActionDelete, meta); err != nil {
			return pv, none, err
		}
		target = n.Key + "." + t.Field
		res := s.afterWrite(ctx, []string{n.Key})
		s.dispatchStrip(ctx, paths, pv.BatchID, target)
		return pv, res, nil
	}

	// Node + subtree, leaf-first (Descendants is depth-first pre-order).
	var changed []string
	for i := len(pv.Nodes) - 1; i >= 0; i-- {
		k := pv.Nodes[i]
		old, _ := snap.Node(k)
		if err := s.store.DeleteNode(ctx, k, old.Version); err != nil {
			s.afterWrite(ctx, changed)
			s.dispatchStrip(ctx, changed, pv.BatchID, target)
			return pv, attributeService.WriteResult{Changed: changed}, err
		}
		m := map[string]any{"batchId": pv.BatchID, "subtreeOf": n.Key}
		if k == n.Key {
			m = meta
		}
		s.record(ctx, actor, domain.EntityAttributeDef, k, domain.ActionDelete, old, nil, m)
		changed = append(changed, k)
	}
	res := s.afterWrite(ctx, changed)
	s.dispatchStrip(ctx, paths, pv.BatchID, target)
	return pv, res, nil
}

// dispatchStrip queues attributes.strip. A failed dispatch is logged: the
// stale values are ignored by the evaluator, and CreateNode / CreateField
// refuse to reuse a key that still has values.
func (s *svc) dispatchStrip(ctx context.Context, paths []string, batchID, target string) {
	if len(paths) == 0 {
		return
	}
	p := attributeService.StripPayload{Paths: paths, BatchID: batchID, Target: target}
	if err := s.dispatchKeyed(context.WithoutCancel(ctx), attributeService.ProcessStrip, attributeService.StripKey(batchID), p); err != nil {
		log.Error("attributes: strip dispatch failed — stale values stay (ignored) until stripped", "target", target, "batch", batchID, "error", err)
	}
}

// Strip removes the deleted paths from every live copy and open revision and
// logs one change_log row per warehouse (D-129). Idempotent: SQS retries and
// re-runs match nothing already stripped.
func (s *svc) Strip(ctx context.Context, p attributeService.StripPayload) error {
	ids, err := s.store.Strip(ctx, p.Paths, s.now().UTC())
	for _, id := range ids {
		s.record(ctx, domain.SystemActor, domain.EntityWarehouse, id.Hex(), domain.ActionUpdate, nil,
			map[string]any{"removed": p.Paths},
			map[string]any{"op": "strip", "target": p.Target, "batchId": p.BatchID})
	}
	log.Info("attributes: strip done", "target", p.Target, "batch", p.BatchID, "warehouses", len(ids))
	if err != nil {
		return fmt.Errorf("strip %s: %w", p.Target, err)
	}
	return nil
}

// checkKeyFree refuses to reuse a deleted key whose values haven't been
// stripped yet (they would resurface under the new definition).
func (s *svc) checkKeyFree(ctx context.Context, path, label string) error {
	c, err := s.store.CountHolding(ctx, []string{path})
	if err != nil {
		return err
	}
	if c > 0 {
		return attributeService.Invalidf("%s still has values on %d warehouses from an earlier delete; retry once the strip job has run", label, c)
	}
	return nil
}

// industryRefs lists industries whose rules name node / node.field / option.
func industryRefs(snap *domain.Snapshot, node, field, option string) []string {
	var out []string
	for i := range snap.Industries {
		if domain.IndustryReferences(&snap.Industries[i], node, field, option) {
			out = append(out, "industry "+snap.Industries[i].Key)
		}
	}
	return out
}

// ratioRefs lists the surviving ratio fields whose top or bottom is a
// "<node>.<field>" path gone(p) reports as deleted. A ratio field that is
// itself deleted doesn't count.
func ratioRefs(snap *domain.Snapshot, gone func(path string) bool) []string {
	var out []string
	for _, n := range snap.Nodes {
		for _, f := range n.Fields {
			path := n.Key + "." + f.Key
			if f.Ratio == nil || gone(path) {
				continue
			}
			if gone(f.Ratio.Top) || gone(f.Ratio.Bottom) {
				out = append(out, "ratio field "+path)
			}
		}
	}
	return out
}

func nodeOf(path string) string {
	k, _, _ := strings.Cut(path, ".")
	return k
}

func dedupe(in []string) []string {
	out := []string{}
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
