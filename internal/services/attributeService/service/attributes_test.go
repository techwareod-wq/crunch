package service

import (
	"context"
	"errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"slices"
	"testing"

	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

func coldStorage() models.AttributeNode {
	return models.AttributeNode{Key: "cold_storage", ParentKey: domain.RootKey, Name: "Cold storage", Public: true, Filterable: true,
		Fields: []models.AttributeField{
			{Key: "temperature", Name: "Temperature", Type: domain.TypeNumber, Required: true, Public: true, Filterable: true,
				Unit: &models.UnitSpec{Family: domain.DimTemp}},
			{Key: "temp_type", Name: "Type", Type: domain.TypePick, Public: true, Filterable: true,
				Options: []models.FieldOption{{Key: "chilled", Label: "Chilled"}, {Key: "frozen", Label: "Frozen"}}},
		}}
}

func isValidation(err error) bool {
	var ve *attributeService.ValidationError
	return errors.As(err, &ve)
}

func TestEnsureRoot(t *testing.T) {
	h := newHarness()
	if err := h.svc.EnsureRoot(ctx); err != nil {
		t.Fatal(err)
	}
	root := h.node(domain.RootKey)
	if !root.System || len(root.Fields) != len(domain.RootNode().Fields) {
		t.Fatalf("root = %+v", root)
	}
	if len(h.sent) != 1 || len(h.cl.Entries) != 1 {
		t.Fatalf("first boot: sent %d, logged %d", len(h.sent), len(h.cl.Entries))
	}

	// Idempotent: a second boot writes nothing.
	h.sent, h.cl.Entries = nil, nil
	if err := h.svc.EnsureRoot(ctx); err != nil || len(h.sent)+len(h.cl.Entries) != 0 {
		t.Fatalf("second boot wrote: %v sent=%d logged=%d", err, len(h.sent), len(h.cl.Entries))
	}

	// An admin rename survives; a missing system field is re-added.
	f, _ := root.Field("name")
	g := f.Clone()
	g.Name = "Listing title"
	if _, _, err := h.svc.UpdateField(ctx, actor, domain.RootKey, root.Version, g); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	n := h.store.nodes[domain.RootKey]
	n.Fields = slices.DeleteFunc(n.Fields, func(f models.AttributeField) bool { return f.Key == "rent" })
	h.store.nodes[domain.RootKey] = n
	h.store.mu.Unlock()
	if err := h.svc.EnsureRoot(ctx); err != nil {
		t.Fatal(err)
	}
	root = h.node(domain.RootKey)
	if f, _ := root.Field("name"); f.Name != "Listing title" {
		t.Errorf("rename lost: %q", f.Name)
	}
	if _, ok := root.Field("rent"); !ok {
		t.Error("rent not re-added")
	}
}

func TestCreateNode(t *testing.T) {
	h := newHarness().booted()
	if _, _, err := h.svc.CreateNode(ctx, actor, coldStorage(), ""); !isValidation(err) {
		t.Fatalf("missing default: %v", err)
	}
	n, res, err := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if n.Version != 1 || n.Order != 1 || n.UpdatedBy != actor.Email || res.RulesVersion != 2 {
		t.Errorf("node = %+v, res = %+v", n, res)
	}
	if len(h.sent) != 1 || h.sent[0].key != attributeService.RecomputeAllKey(2, attributeService.RunRules) {
		t.Errorf("recompute not dispatched: %+v", h.sent)
	}
	if e := h.cl.Entries[0]; e.Action != domain.ActionCreate || e.Meta["default"] != "unknown" {
		t.Errorf("change log = %+v", e)
	}
	if h.cache.Snapshot().Version != 2 {
		t.Error("cache not reloaded")
	}
	if _, _, err := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo); !errors.Is(err, attributeService.ErrKeyExists) {
		t.Errorf("duplicate: %v", err)
	}
	bad := coldStorage()
	bad.Key, bad.ParentKey = "x", "ghost"
	if _, _, err := h.svc.CreateNode(ctx, actor, bad, attributeService.DefaultNo); !isValidation(err) {
		t.Errorf("unknown parent: %v", err)
	}
	locked := coldStorage()
	locked.Key = "y"
	locked.Fields[0].Locked = true
	n, _, err = h.svc.CreateNode(ctx, actor, locked, attributeService.DefaultNo)
	if err != nil || n.Fields[0].Locked {
		t.Errorf("admin-created field came out locked: %v", err)
	}
}

func TestUpdateNodeCAS(t *testing.T) {
	h := newHarness().booted()
	n, _, _ := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo)
	name := "Cold chain"
	upd, _, err := h.svc.UpdateNode(ctx, actor, attributeService.NodePatch{Key: n.Key, ExpectedVersion: n.Version, Name: &name})
	if err != nil || upd.Name != name || upd.Version != 2 {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if _, _, err := h.svc.UpdateNode(ctx, actor, attributeService.NodePatch{Key: n.Key, ExpectedVersion: n.Version, Name: &name}); !errors.Is(err, attributeService.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	if _, _, err := h.svc.UpdateNode(ctx, actor, attributeService.NodePatch{Key: "ghost"}); !errors.Is(err, attributeService.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestMoveAndReorder(t *testing.T) {
	h := newHarness().booted()
	cold, _, _ := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo)
	tc := models.AttributeNode{Key: "temp_control", ParentKey: "cold_storage", Name: "Temp control"}
	tc, _, _ = h.svc.CreateNode(ctx, actor, tc, attributeService.DefaultNo)
	haz := models.AttributeNode{Key: "hazmat", ParentKey: domain.RootKey, Name: "Hazmat"}
	if _, _, err := h.svc.CreateNode(ctx, actor, haz, attributeService.DefaultNo); err != nil {
		t.Fatal(err)
	}

	if _, _, err := h.svc.MoveNode(ctx, actor, "cold_storage", cold.Version, "temp_control", 0); !isValidation(err) {
		t.Errorf("cycle: %v", err)
	}
	root := h.node(domain.RootKey)
	if _, _, err := h.svc.MoveNode(ctx, actor, domain.RootKey, root.Version, "hazmat", 0); !isValidation(err) {
		t.Errorf("root move: %v", err)
	}
	moved, _, err := h.svc.MoveNode(ctx, actor, "temp_control", tc.Version, "hazmat", 0)
	if err != nil || moved.ParentKey != "hazmat" || moved.Order != 1 {
		t.Fatalf("move: %v %+v", err, moved)
	}

	if _, err := h.svc.ReorderNodes(ctx, actor, domain.RootKey, []string{"hazmat"}); !isValidation(err) {
		t.Errorf("partial reorder: %v", err)
	}
	res, err := h.svc.ReorderNodes(ctx, actor, domain.RootKey, []string{"hazmat", "cold_storage"})
	if err != nil || !slices.Equal(res.Changed, []string{"hazmat", "cold_storage"}) {
		t.Fatalf("reorder: %v %+v", err, res)
	}
	snap, _ := h.store.Load(ctx)
	if !slices.Equal(snap.Children(domain.RootKey), []string{"hazmat", "cold_storage"}) {
		t.Errorf("children = %v", snap.Children(domain.RootKey))
	}
}

func TestFields(t *testing.T) {
	h := newHarness().booted()
	n, _, _ := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo)

	hum := models.AttributeField{Key: "humidity", Name: "Humidity", Type: domain.TypeNumber,
		Validations: []models.FieldValidation{{Kind: domain.ValidMax, Value: 100.0}}}
	n, _, err := h.svc.CreateField(ctx, actor, n.Key, n.Version, hum)
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := n.Field("humidity"); f.Order != 3 {
		t.Errorf("appended order = %d", f.Order)
	}
	if _, _, err := h.svc.CreateField(ctx, actor, n.Key, n.Version, hum); !errors.Is(err, attributeService.ErrKeyExists) {
		t.Errorf("duplicate field: %v", err)
	}
	badRe := models.AttributeField{Key: "code", Name: "Code", Type: domain.TypeText, Validations: []models.FieldValidation{{Kind: domain.ValidRegex, Value: "("}}}
	if _, _, err := h.svc.CreateField(ctx, actor, n.Key, n.Version, badRe); !isValidation(err) {
		t.Errorf("bad regex saved: %v", err)
	}

	// Type immutable (D-130); option removal only via delete (D-138).
	f, _ := n.Field("temp_type")
	g := f.Clone()
	g.Type = domain.TypeMulti
	if _, _, err := h.svc.UpdateField(ctx, actor, n.Key, n.Version, g); !isValidation(err) {
		t.Errorf("retype: %v", err)
	}
	g = f.Clone()
	g.Options = g.Options[:1]
	if _, _, err := h.svc.UpdateField(ctx, actor, n.Key, n.Version, g); !isValidation(err) {
		t.Errorf("option removed: %v", err)
	}
	g = f.Clone()
	g.Required = true
	g.Options = append(g.Options, models.FieldOption{Key: "ambient", Label: "Ambient"})
	if n, _, err = h.svc.UpdateField(ctx, actor, n.Key, n.Version, g); err != nil {
		t.Fatalf("legit update: %v", err)
	}

	// Locked root fields: rename yes, anything else no (D-140).
	root := h.node(domain.RootKey)
	area, _ := root.Field("total_area")
	a := area.Clone()
	a.Filterable = false
	if _, _, err := h.svc.UpdateField(ctx, actor, domain.RootKey, root.Version, a); !isValidation(err) {
		t.Errorf("locked field edited: %v", err)
	}
	a = area.Clone()
	a.Locked = false // can't be unlocked through the API either
	a.Name = "Built-up area"
	if root, _, err = h.svc.UpdateField(ctx, actor, domain.RootKey, root.Version, a); err != nil {
		t.Fatalf("locked rename: %v", err)
	}
	if f, _ := root.Field("total_area"); !f.Locked || f.Name != "Built-up area" {
		t.Errorf("total_area = %+v", f)
	}

	if _, _, err := h.svc.ReorderFields(ctx, actor, n.Key, n.Version, []string{"humidity", "temp_type"}); !isValidation(err) {
		t.Errorf("partial field reorder: %v", err)
	}
	n, _, err = h.svc.ReorderFields(ctx, actor, n.Key, n.Version, []string{"humidity", "temp_type", "temperature"})
	if err != nil || n.Fields[0].Key != "humidity" {
		t.Fatalf("field reorder: %v %+v", err, n.Fields)
	}
}

func TestIndustries(t *testing.T) {
	h := newHarness().booted()
	h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo)
	ind := models.Industry{Key: "food", Name: "Food",
		Required:  []models.Condition{{Node: "cold_storage", Cmp: domain.CmpIsYes}},
		Preferred: []models.Condition{{Node: "cold_storage", Field: "temperature", Cmp: domain.CmpLte, Value: -18.0}}}
	got, _, err := h.svc.CreateIndustry(ctx, actor, ind)
	if err != nil || got.Version != 1 || got.Order != 1 {
		t.Fatalf("create: %v %+v", err, got)
	}
	bad := ind
	bad.Key = "bad"
	bad.Required = []models.Condition{{Node: "ghost", Cmp: domain.CmpIsYes}}
	if _, _, err := h.svc.CreateIndustry(ctx, actor, bad); !isValidation(err) {
		t.Errorf("bad rule: %v", err)
	}
	name := "Food & beverage"
	upd, _, err := h.svc.UpdateIndustry(ctx, actor, attributeService.IndustryPatch{Key: "food", ExpectedVersion: 1, Name: &name})
	if err != nil || upd.Version != 2 || len(upd.Preferred) != 1 {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if _, err := h.svc.DeleteIndustry(ctx, actor, "food", 1); !errors.Is(err, attributeService.ErrVersionConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if _, err := h.svc.DeleteIndustry(ctx, actor, "food", 2); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.cache.Snapshot().Industry("food"); ok {
		t.Error("industry still cached")
	}
	last := h.cl.Entries[len(h.cl.Entries)-1]
	if last.Action != domain.ActionDelete || last.After != nil {
		t.Errorf("delete log = %+v", last)
	}
}

func TestTailFailuresDontFailWrite(t *testing.T) {
	h := newHarness().booted()
	h.failNext = errBoom
	if _, _, err := h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo); err != nil {
		t.Fatalf("dispatch failure surfaced: %v", err)
	}
	h.store.failReplace = errBoom
	n := h.node("cold_storage")
	name := "x"
	if _, _, err := h.svc.UpdateNode(ctx, actor, attributeService.NodePatch{Key: n.Key, ExpectedVersion: n.Version, Name: &name}); !errors.Is(err, errBoom) {
		t.Fatalf("store failure hidden: %v", err)
	}
}

func TestRecompute(t *testing.T) {
	h := newHarness().booted()
	h.svc.CreateNode(ctx, actor, coldStorage(), attributeService.DefaultNo)
	h.svc.CreateIndustry(ctx, actor, models.Industry{Key: "food", Name: "Food",
		Required: []models.Condition{{Node: "cold_storage", Cmp: domain.CmpIsYes}}})
	v := h.cache.Snapshot().Version

	yes := models.Attributes{"cold_storage": {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{"temperature": {V: -20.0}}}}
	ids := []any{
		h.warehouses.add(models.WarehouseLive, yes),
		h.warehouses.add(models.WarehouseLive, models.Attributes{}),
		h.warehouses.add(models.WarehouseArchived, models.Attributes{"cold_storage": {Status: domain.StatusUnknown}}),
	}
	h.warehouses.add(models.WarehouseUnpublished, yes) // never evaluated
	h.sent = nil

	if err := h.svc.RecomputeAll(ctx, attributeService.RecomputeAllPayload{RulesVersion: v, Run: attributeService.RunRules}); err != nil {
		t.Fatal(err)
	}
	if len(h.sent) != 2 { // 3 stale docs, batch size 2
		t.Fatalf("batches = %d", len(h.sent))
	}
	for _, d := range h.sent {
		if err := h.svc.RecomputeBatch(ctx, d.payload.(attributeService.RecomputeBatchPayload)); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.warehouses.proj) != len(ids) {
		t.Fatalf("projections = %d", len(h.warehouses.proj))
	}
	fits := map[string]int{}
	for _, p := range h.warehouses.proj {
		if p.FitRulesVersion != v {
			t.Errorf("version = %d", p.FitRulesVersion)
		}
		fits[p.Fit[0]]++
	}
	if fits["food:F"] != 1 || fits["food:N"] != 1 || fits["food:U"] != 1 {
		t.Errorf("fits = %v", fits)
	}

	// Nothing stale now; an older message is dropped.
	h.sent = nil
	if err := h.svc.RecomputeAll(ctx, attributeService.RecomputeAllPayload{RulesVersion: v, Run: attributeService.RunRules}); err != nil || len(h.sent) != 0 {
		t.Errorf("re-run dispatched %d", len(h.sent))
	}
	h.store.BumpRulesVersion(ctx)
	if err := h.svc.RecomputeAll(ctx, attributeService.RecomputeAllPayload{RulesVersion: v, Run: attributeService.RunRules}); err != nil || len(h.sent) != 0 {
		t.Errorf("superseded run dispatched %d", len(h.sent))
	}
}
