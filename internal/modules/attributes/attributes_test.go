package attributes

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/modules/attributes/seed"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

func TestSeedIsValidAndIdempotent(t *testing.T) {
	h := newHarness()
	dry, err := h.svc.Seed(ctx, actor, false)
	if err != nil {
		t.Fatalf("seed tree fails its own validation: %v", err)
	}
	if !dry.DryRun || len(dry.CreatedDefs) != len(seed.Defs()) || len(dry.CreatedIndustries) != len(seed.Industries()) {
		t.Fatalf("dry run = %+v", dry)
	}
	if v, _ := h.store.RulesVersion(ctx); v != 0 || len(h.store.defs) != 0 {
		t.Fatal("dry run wrote")
	}

	rep, err := h.svc.Seed(ctx, actor, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.CreatedDefs) != len(seed.Defs()) || rep.RulesVersion != 1 {
		t.Fatalf("apply = %+v", rep)
	}
	if len(h.sent) != 1 || h.sent[0].key != "recompute_all:1:rules" {
		t.Fatalf("dispatches = %+v", h.sent)
	}
	if got := len(h.cl.Entries); got != len(seed.Defs())+len(seed.Industries()) {
		t.Fatalf("change_log entries = %d", got)
	}
	if h.cache.Snapshot().Version != 1 {
		t.Fatal("cache not reloaded")
	}

	again, err := h.svc.Seed(ctx, actor, true)
	if err != nil || len(again.CreatedDefs) != 0 || len(again.CreatedIndustries) != 0 || again.RulesVersion != 1 {
		t.Fatalf("re-run not a no-op: %+v %v", again, err)
	}
}

// The seeded rules produce the expected verdicts for a realistic pharma site.
func TestSeedEvaluatesPharmaSite(t *testing.T) {
	h := newHarness()
	h.seed()
	yes := domain.Answer{Status: domain.StatusKnown, V: true}
	in := domain.EvalInput{TotalAreaSqm: 50000 * domain.SqmPerSqft, Attributes: map[string]domain.Answer{
		"cold_storage": yes, "gdp_compliant": yes, "temp_tracking": yes, "fire_noc": yes, "power_backup": yes,
		"zone_segregation": {Status: domain.StatusKnown, V: []string{"quarantine", "received", "waste"}},
		"dock_doors":       {Status: domain.StatusKnown, V: 10.0},
		"hazmat_storage":   {Status: domain.StatusKnown, V: false},
	}}
	r := domain.Evaluate(h.cache.Snapshot(), in, time.Now())
	if r.Fit["pharma"] != domain.VerdictFit {
		t.Fatalf("pharma = %s", r.Fit["pharma"])
	}
	if r.Fit["chemicals"] != domain.VerdictNotFit { // DG consent not applicable → F
		t.Fatalf("chemicals = %s", r.Fit["chemicals"])
	}
	if r.Fit["textiles"] != domain.VerdictFit || r.Fit["bonded"] != domain.VerdictUnverified {
		t.Fatalf("fit = %v", r.Fit)
	}
	if r.Calc["dock_ratio"] != 2 {
		t.Fatalf("dock ratio = %v", r.Calc["dock_ratio"])
	}
	if slices.Contains(r.NeedsInfo, "dg_classes") || !slices.Contains(r.NeedsInfo, "deep_freeze") {
		t.Fatalf("needsInfo = %v", r.NeedsInfo)
	}
}

func TestCreateDef(t *testing.T) {
	h := newHarness()
	h.seed()
	d, res, err := h.svc.CreateDef(ctx, actor, domain.AttrDef{Key: "mezzanine", Kind: domain.KindAttribute, Type: domain.TypeBool, Name: "Mezzanine", ParentKey: "infrastructure"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Order == 0 || d.UpdatedBy != actor.Email || res.RulesVersion != 2 {
		t.Fatalf("created = %+v res=%+v", d, res)
	}
	if len(h.cl.Entries) != 1 || h.cl.Entries[0].Action != domain.ActionCreate || h.cl.Entries[0].Before != nil {
		t.Fatalf("change_log = %+v", h.cl.Entries)
	}
	if len(h.sent) != 1 || h.sent[0].key != "recompute_all:2:rules" {
		t.Fatalf("dispatch = %+v", h.sent)
	}

	if _, _, err := h.svc.CreateDef(ctx, actor, domain.AttrDef{Key: "mezzanine", Kind: domain.KindAttribute, Type: domain.TypeBool, Name: "x"}); !errors.Is(err, errKeyExists) {
		t.Fatalf("dup key: %v", err)
	}
	var ve *validationError
	if _, _, err := h.svc.CreateDef(ctx, actor, domain.AttrDef{Key: "bad", Kind: domain.KindAttribute, Type: domain.TypeBool, Name: "x", ParentKey: "floor_strength"}); !errors.As(err, &ve) {
		t.Fatalf("bad parent: %v", err)
	}
}

func TestUpdateDefCASAndImmutables(t *testing.T) {
	h := newHarness()
	h.seed()
	name := "Cold store"
	if _, _, err := h.svc.UpdateDef(ctx, actor, DefPatch{Key: "cold_storage", ExpectedVersion: 7, Name: &name}); !errors.Is(err, errVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	d, _, err := h.svc.UpdateDef(ctx, actor, DefPatch{Key: "cold_storage", ExpectedVersion: 1, Name: &name})
	if err != nil || d.Name != name || d.Version != 2 {
		t.Fatalf("update: %+v %v", d, err)
	}
	e := h.cl.Entries[0]
	if e.Action != domain.ActionUpdate || e.Before.(*domain.AttrDef).Name != "Cold storage" {
		t.Fatalf("change_log before = %+v", e.Before)
	}
	// Removing an allowed value is refused.
	vals := []domain.AllowedValue{{Key: "ambient", Label: "Ambient"}}
	var ve *validationError
	if _, _, err := h.svc.UpdateDef(ctx, actor, DefPatch{Key: "temp_type", ExpectedVersion: 1, AllowedValues: &vals}); !errors.As(err, &ve) {
		t.Fatalf("remove option: %v", err)
	}
	// Clearing appliesWhen works.
	d, _, err = h.svc.UpdateDef(ctx, actor, DefPatch{Key: "temp_tracking", ExpectedVersion: 1, ClearAppliesWhen: true})
	if err != nil || d.AppliesWhen != nil {
		t.Fatalf("clear appliesWhen: %v", err)
	}
	// The snapshot the cache serves was not mutated by the service.
	if cs, _ := h.cache.Snapshot().Def("temp_type"); len(cs.AllowedValues) != 3 {
		t.Fatal("cached snapshot mutated")
	}
}

func TestMoveDef(t *testing.T) {
	h := newHarness()
	h.seed()
	var ve *validationError
	if _, _, err := h.svc.MoveDef(ctx, actor, "cold_storage", "deep_freeze", 0); !errors.As(err, &ve) {
		t.Fatalf("move under own child: %v", err)
	}
	d, _, err := h.svc.MoveDef(ctx, actor, "cctv", "compliance", 0)
	if err != nil || d.ParentKey != "compliance" || h.cl.Entries[0].Action != domain.ActionMove {
		t.Fatalf("move: %+v %v", d, err)
	}
	if _, _, err := h.svc.MoveDef(ctx, actor, "nope", "", 0); !errors.Is(err, errNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestReorder(t *testing.T) {
	h := newHarness()
	h.seed()
	kids := h.cache.Snapshot().Children("customs")
	if len(kids) != 1 {
		t.Fatalf("customs children = %v", kids)
	}
	roots := slices.Clone(h.cache.Snapshot().Children(""))
	slices.Reverse(roots)
	res, err := h.svc.Reorder(ctx, actor, "", roots)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.cache.Snapshot().Children(""); !slices.Equal(got, roots) {
		t.Fatalf("order = %v want %v (changed %v)", got, roots, res.Changed)
	}
	var ve *validationError
	if _, err := h.svc.Reorder(ctx, actor, "", roots[:2]); !errors.As(err, &ve) {
		t.Fatalf("partial list: %v", err)
	}
}

func TestRetireGuardAndConfirm(t *testing.T) {
	h := newHarness()
	h.seed()

	// D-042: pharma requires cold_storage (and temp_tracking under it).
	_, err := h.svc.RetireDef(ctx, actor, "cold_storage", true)
	var be *retireBlockedError
	if !errors.As(err, &be) || !slices.Contains(be.Industries, "pharma") || !slices.Contains(be.Industries, "fmcg") {
		t.Fatalf("guard: %v %+v", err, be)
	}

	// A leaf nobody references retires directly.
	if res, err := h.svc.RetireDef(ctx, actor, "defreeze", false); err != nil || !slices.Equal(res.Changed, []string{"defreeze"}) {
		t.Fatalf("leaf retire: %+v %v", res, err)
	}

	// deep_freeze has min_temp below it → confirm needed, then bottom-up.
	_, err = h.svc.RetireDef(ctx, actor, "deep_freeze", false)
	var ce *confirmRequiredError
	if !errors.As(err, &ce) || !slices.Equal(ce.Descendants, []string{"min_temp"}) {
		t.Fatalf("confirm: %v", err)
	}
	res, err := h.svc.RetireDef(ctx, actor, "deep_freeze", true)
	if err != nil || !slices.Equal(res.Changed, []string{"min_temp", "deep_freeze"}) {
		t.Fatalf("retire with confirm: %+v %v", res, err)
	}
	if d, _ := h.cache.Snapshot().Def("min_temp"); !d.Retired {
		t.Fatal("descendant not retired")
	}
	// Retired attrs leave needsInfo.
	r := domain.Evaluate(h.cache.Snapshot(), domain.EvalInput{Attributes: map[string]domain.Answer{"cold_storage": {Status: domain.StatusKnown, V: true}}}, time.Now())
	if slices.Contains(r.NeedsInfo, "deep_freeze") {
		t.Fatal("retired attr still flagged")
	}

	// appliesWhen references block too: gdp_compliant feeds temp_tracking.
	if _, err := h.svc.RetireDef(ctx, actor, "gdp_compliant", false); !errors.As(err, &be) || !slices.Contains(be.Attributes, "temp_tracking") {
		t.Fatalf("appliesWhen guard: %v", err)
	}
}

func TestRestoreNeedsActiveParent(t *testing.T) {
	h := newHarness()
	h.seed()
	if _, err := h.svc.RetireDef(ctx, actor, "deep_freeze", true); err != nil {
		t.Fatal(err)
	}
	var ve *validationError
	if _, _, err := h.svc.RestoreDef(ctx, actor, "min_temp"); !errors.As(err, &ve) {
		t.Fatalf("restore under retired parent: %v", err)
	}
	if d, _, err := h.svc.RestoreDef(ctx, actor, "deep_freeze"); err != nil || d.Retired {
		t.Fatalf("restore: %v", err)
	}
	if d, _, err := h.svc.RestoreDef(ctx, actor, "min_temp"); err != nil || d.Retired {
		t.Fatalf("restore child: %v", err)
	}
}

func TestIndustryWrites(t *testing.T) {
	h := newHarness()
	h.seed()
	ind, _, err := h.svc.CreateIndustry(ctx, actor, domain.Industry{Key: "cold_chain", Name: "Cold chain",
		Required: []domain.Condition{{Attr: "temp_type", Cmp: domain.CmpIn, Value: []any{"chilled", "frozen"}}}})
	if err != nil || ind.Order != 10 {
		t.Fatalf("create: %+v %v", ind, err)
	}
	if _, ok := ind.Required[0].Value.([]string); !ok {
		t.Fatalf("value not normalized: %T", ind.Required[0].Value)
	}
	var ve *validationError
	bad := []domain.Condition{{Attr: "nope", Cmp: domain.CmpEq, Value: true}}
	if _, _, err := h.svc.UpdateIndustry(ctx, actor, IndustryPatch{Key: "cold_chain", ExpectedVersion: 1, Required: &bad}); !errors.As(err, &ve) {
		t.Fatalf("bad rule: %v", err)
	}
	heavy := []domain.Condition{{Attr: "floor_strength", Cmp: domain.CmpGte, Value: 6.0}}
	if _, _, err := h.svc.UpdateIndustry(ctx, actor, IndustryPatch{Key: "heavy", ExpectedVersion: 1, Required: &heavy}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.UpdateIndustry(ctx, actor, IndustryPatch{Key: "heavy", ExpectedVersion: 1, Required: &heavy}); !errors.Is(err, errVersionConflict) {
		t.Fatalf("stale: %v", err)
	}

	// Retiring the industry lifts the D-042 block on its attributes.
	if _, _, err := h.svc.SetIndustryRetired(ctx, actor, "bonded", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RetireDef(ctx, actor, "bonded", true); err != nil {
		t.Fatalf("retire after industry retired: %v", err)
	}
	// …and restoring the industry now fails: its rule names a retired attr.
	if _, _, err := h.svc.SetIndustryRetired(ctx, actor, "bonded", false); !errors.As(err, &ve) {
		t.Fatalf("restore with retired attr: %v", err)
	}
}

func TestDispatchFailureDoesNotFailWrite(t *testing.T) {
	h := newHarness()
	h.seed()
	h.failNext = errBoom
	name := "x"
	if _, res, err := h.svc.UpdateDef(ctx, actor, DefPatch{Key: "cctv", ExpectedVersion: 1, Name: &name}); err != nil || res.RulesVersion != 2 {
		t.Fatalf("write failed on dispatch error: %v", err)
	}
}

func TestRecomputeFanOutAndVersionGuard(t *testing.T) {
	h := newHarness()
	h.seed()
	yes := domain.Answer{Status: domain.StatusKnown, V: true}
	for i := 0; i < 5; i++ {
		h.warehouses.add(domain.WarehouseLive, map[string]domain.Answer{"cold_storage": yes})
	}
	h.warehouses.add(domain.WarehouseUnpublished, nil)

	if err := h.rc.all(ctx, RecomputeAllPayload{RulesVersion: 1, Run: runRules}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, d := range h.sent {
		keys = append(keys, d.key)
	}
	if !slices.Equal(keys, []string{"recompute:1:rules:1", "recompute:1:rules:2", "recompute:1:rules:3"}) {
		t.Fatalf("batch keys = %v", keys)
	}
	for _, d := range h.sent {
		if err := h.rc.batch(ctx, d.payload.(RecomputeBatchPayload)); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.warehouses.proj) != 5 {
		t.Fatalf("projections = %d, want 5 (unpublished skipped)", len(h.warehouses.proj))
	}
	for _, p := range h.warehouses.proj {
		if p.FitRulesVersion != 1 || !slices.Contains(p.Chips, "cold_storage:yes") {
			t.Fatalf("projection = %+v", p)
		}
	}

	// A new rules write → v2. A stale v1 batch arriving late must not
	// overwrite v2 projections.
	name := "Cold store"
	if _, _, err := h.svc.UpdateDef(ctx, actor, DefPatch{Key: "cold_storage", ExpectedVersion: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	h.sent = nil
	if err := h.rc.all(ctx, RecomputeAllPayload{RulesVersion: 2}); err != nil {
		t.Fatal(err)
	}
	for _, d := range h.sent {
		_ = h.rc.batch(ctx, d.payload.(RecomputeBatchPayload))
	}
	old := domain.Projection{FitRulesVersion: 1}
	for id := range h.warehouses.proj {
		if n, _ := h.warehouses.WriteProjections(ctx, map[primitive.ObjectID]domain.Projection{id: old}); n != 0 {
			t.Fatal("older projection overwrote a newer one")
		}
	}
	for _, p := range h.warehouses.proj {
		if p.FitRulesVersion != 2 {
			t.Fatalf("not recomputed to v2: %d", p.FitRulesVersion)
		}
	}

	// A superseded recompute_all is dropped; nothing stale is left anyway.
	h.sent = nil
	if err := h.rc.all(ctx, RecomputeAllPayload{RulesVersion: 1}); err != nil || len(h.sent) != 0 {
		t.Fatalf("superseded run dispatched %d", len(h.sent))
	}
}

func TestBatchWaitsForNewerSnapshot(t *testing.T) {
	h := newHarness()
	h.seed() // v1 in store and cache
	// Another instance bumped to v2; this cache hasn't seen it yet.
	h.store.v = 2
	s, err := h.cache.SnapshotAtLeast(ctx, 2)
	if err != nil || s.Version != 2 {
		t.Fatalf("reload on demand: %v", err)
	}
	if _, err := h.cache.SnapshotAtLeast(ctx, 3); err == nil {
		t.Fatal("snapshot older than requested returned")
	}
}

func TestSafetyNetKeysDoNotCollide(t *testing.T) {
	h := newHarness()
	h.seed()
	units, err := h.rc.resolveSafetyNet(ctx, cron.Occurrence{Job: JobRecomputeSafetyNet, At: time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC)})
	if err != nil || len(units) != 1 {
		t.Fatal(err)
	}
	p := units[0].Payload.(RecomputeAllPayload)
	if p.RulesVersion != 1 || p.Run != "sn-20261002" || units[0].IdempotencyKey != "recompute_all:1:sn-20261002" {
		t.Fatalf("unit = %+v", units[0])
	}
}

// The safety-net job must pass the cron registry's boot-time validation.
func TestCronJobRegisters(t *testing.T) {
	m := New(&config.AppContext{})
	_, err := cron.BuildJobRegistry(m.CronJobs(), config.CronValues{
		TickSeconds: 300, ClaimStaleSeconds: 600, DefaultZone: "Asia/Kolkata",
		Jobs: map[string]config.CronJobValues{string(JobRecomputeSafetyNet): {Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
