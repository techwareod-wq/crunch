package domain

import (
	"math"
	"slices"
	"testing"
	"time"
)

// fixture:
//
//	warehouse (root, RootNode fields)
//	├─ infrastructure: dock_doors (number), dock_ratio (ratio, per 10,000 sq ft)
//	├─ cold_storage: temperature* (°C), humidity, temp_type (pick)
//	│   └─ temp_control: temperature_range* (°C)
//	└─ hazmat: dg_classes* (multi), owner_consent (bool)
func fixtureNodes() []Node {
	pub := func(f Field) Field { f.Public, f.Filterable = true, true; return f }
	temp := &UnitSpec{Family: DimTemp, Input: []string{UnitC, UnitF}}
	return []Node{
		RootNode(),
		{Key: "infrastructure", ParentKey: RootKey, Name: "Infrastructure", Order: 1, Public: true, Fields: []Field{
			pub(Field{Key: "dock_doors", Name: "Dock doors", Type: TypeNumber, Unit: &UnitSpec{Family: DimCount}, Order: 1}),
			pub(Field{Key: "dock_ratio", Name: "Dock ratio", Type: TypeRatio, Order: 2,
				Ratio: &RatioSpec{Top: "infrastructure.dock_doors", Bottom: "warehouse.total_area", Per: 10000, BottomUnit: UnitSqft}}),
		}},
		{Key: "cold_storage", ParentKey: RootKey, Name: "Cold storage", Order: 2, Public: true, Filterable: true, Fields: []Field{
			pub(Field{Key: "temperature", Name: "Temperature", Type: TypeNumber, Unit: temp, Required: true, Order: 1}),
			pub(Field{Key: "humidity", Name: "Humidity", Type: TypeNumber, Order: 2}),
			pub(Field{Key: "temp_type", Name: "Temperature type", Type: TypePick, Order: 3, Options: []Option{
				{Key: "chilled", Label: "Chilled"}, {Key: "frozen", Label: "Frozen"}}}),
		}},
		{Key: "temp_control", ParentKey: "cold_storage", Name: "Temperature control", Order: 1, Public: true, Filterable: true, Fields: []Field{
			pub(Field{Key: "temperature_range", Name: "Range", Type: TypeRange, Unit: temp, Required: true, Order: 1}),
		}},
		{Key: "hazmat", ParentKey: RootKey, Name: "Hazmat", Order: 3, Public: true, Filterable: true, Fields: []Field{
			pub(Field{Key: "dg_classes", Name: "DG classes", Type: TypeMulti, Required: true, Order: 1, Options: []Option{
				{Key: "c3", Label: "3"}, {Key: "c8", Label: "8"}}}),
			pub(Field{Key: "owner_consent", Name: "Owner consent", Type: TypeBool, Order: 2}),
		}},
	}
}

func fixtureSnap(inds ...Industry) *Snapshot { return NewSnapshot(7, fixtureNodes(), inds) }

func val(v any) *FieldValue { return &FieldValue{V: v} }

// baseAttrs: a complete live listing, 10,000 sq ft, cold storage yes.
func baseAttrs() Attributes {
	return Attributes{
		RootKey: {Status: StatusYes, Fields: map[string]*FieldValue{
			"total_area": val(Area{Value: 10000, Unit: UnitSqft, Sqm: 10000 * SqmPerSqft}),
		}},
		"infrastructure": {Status: StatusYes, Fields: map[string]*FieldValue{"dock_doors": val(4.0)}},
		"cold_storage": {Status: StatusYes, Fields: map[string]*FieldValue{
			"temperature": val(-18.0), "humidity": nil, "temp_type": val("frozen"),
		}},
	}
}

func TestEffectiveState(t *testing.T) {
	s := fixtureSnap()
	a := Attributes{
		"cold_storage": {Status: StatusUnknown},
		"temp_control": {Status: StatusYes}, // parent not yes → forced no
		"hazmat":       {Status: StatusYes},
	}
	r := Evaluate(s, a, time.Time{})
	want := map[string]NodeStatus{RootKey: StatusYes, "infrastructure": StatusNo, "cold_storage": StatusUnknown, "temp_control": StatusNo, "hazmat": StatusYes}
	for k, w := range want {
		if r.State[k] != w {
			t.Errorf("%s = %s, want %s", k, r.State[k], w)
		}
	}
}

func TestConditionTable(t *testing.T) {
	s := fixtureSnap()
	cases := []struct {
		name  string
		edit  func(Attributes)
		cond  Condition
		want  Tri
		about string
	}{
		{"is_yes yes", nil, Condition{Node: "cold_storage", Cmp: CmpIsYes}, TriTrue, ""},
		{"is_yes absent", nil, Condition{Node: "hazmat", Cmp: CmpIsYes}, TriFalse, "D-133 absent = no"},
		{"is_yes unknown", func(a Attributes) { a["hazmat"] = NodeState{Status: StatusUnknown} }, Condition{Node: "hazmat", Cmp: CmpIsYes}, TriUnknown, ""},
		{"is_yes ancestor not yes", func(a Attributes) {
			a["cold_storage"] = NodeState{Status: StatusUnknown}
			a["temp_control"] = NodeState{Status: StatusYes}
		}, Condition{Node: "temp_control", Cmp: CmpIsYes}, TriFalse, "D-123: forced no under an unknown parent"},
		{"field on no node", nil, Condition{Node: "hazmat", Field: "owner_consent", Cmp: CmpEq, Value: true}, TriFalse, ""},
		{"field on unknown node", func(a Attributes) { a["hazmat"] = NodeState{Status: StatusUnknown} }, Condition{Node: "hazmat", Field: "owner_consent", Cmp: CmpEq, Value: true}, TriUnknown, ""},
		{"optional null → F", nil, Condition{Node: "cold_storage", Field: "humidity", Cmp: CmpLte, Value: 60.0}, TriFalse, "D-125"},
		{"optional absent → F", func(a Attributes) { delete(a["cold_storage"].Fields, "humidity") }, Condition{Node: "cold_storage", Field: "humidity", Cmp: CmpLte, Value: 60.0}, TriFalse, ""},
		{"required missing → U", func(a Attributes) { delete(a["cold_storage"].Fields, "temperature") }, Condition{Node: "cold_storage", Field: "temperature", Cmp: CmpLte, Value: -10.0}, TriUnknown, "D-127"},
		{"number lte T", nil, Condition{Node: "cold_storage", Field: "temperature", Cmp: CmpLte, Value: -10.0}, TriTrue, ""},
		{"number gte F", nil, Condition{Node: "cold_storage", Field: "temperature", Cmp: CmpGte, Value: 0.0}, TriFalse, ""},
		{"pick in", nil, Condition{Node: "cold_storage", Field: "temp_type", Cmp: CmpIn, Value: []string{"chilled", "frozen"}}, TriTrue, ""},
		{"pick eq F", nil, Condition{Node: "cold_storage", Field: "temp_type", Cmp: CmpEq, Value: "chilled"}, TriFalse, ""},
		{"range can reach", func(a Attributes) {
			a["temp_control"] = NodeState{Status: StatusYes, Fields: map[string]*FieldValue{"temperature_range": val(Range{Min: 2, Max: 8})}}
		}, Condition{Node: "temp_control", Field: "temperature_range", Cmp: CmpLte, Value: 4.0}, TriTrue, "min ≤ v"},
		{"multi contains_all", func(a Attributes) {
			a["hazmat"] = NodeState{Status: StatusYes, Fields: map[string]*FieldValue{"dg_classes": val([]string{"c3", "c8"})}}
		}, Condition{Node: "hazmat", Field: "dg_classes", Cmp: CmpContainsAll, Value: []string{"c3", "c8"}}, TriTrue, ""},
		{"multi in no overlap", func(a Attributes) {
			a["hazmat"] = NodeState{Status: StatusYes, Fields: map[string]*FieldValue{"dg_classes": val([]string{"c3"})}}
		}, Condition{Node: "hazmat", Field: "dg_classes", Cmp: CmpIn, Value: []string{"c8"}}, TriFalse, ""},
		{"area gte", nil, Condition{Node: RootKey, Field: "total_area", Cmp: CmpGte, Value: 900.0}, TriTrue, "sq m"},
		{"ratio computed", nil, Condition{Node: "infrastructure", Field: "dock_ratio", Cmp: CmpGte, Value: 4.0}, TriTrue, ""},
		{"ratio missing input → U", func(a Attributes) { delete(a["infrastructure"].Fields, "dock_doors") }, Condition{Node: "infrastructure", Field: "dock_ratio", Cmp: CmpGte, Value: 1.0}, TriUnknown, "D-134"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := baseAttrs()
			if c.edit != nil {
				c.edit(a)
			}
			r := Evaluate(s, a, time.Time{})
			e := &evaluator{s: s, a: a, eff: r.State, ratios: r.Ratios}
			if got := e.cond(c.cond); got != c.want {
				t.Fatalf("got %d, want %d %s", got, c.want, c.about)
			}
		})
	}
}

func TestDockRatio(t *testing.T) {
	r := Evaluate(fixtureSnap(), baseAttrs(), time.Time{})
	v, ok := r.Ratios["infrastructure.dock_ratio"]
	if !ok || math.Abs(v-4) > 1e-9 {
		t.Fatalf("dock ratio = %v (%v), want 4 doors per 10,000 sq ft", v, ok)
	}
}

func TestVerdicts(t *testing.T) {
	ind := func(key string, req, pref []Condition) Industry {
		return Industry{Key: key, Name: key, Required: req, Preferred: pref}
	}
	coldYes := Condition{Node: "cold_storage", Cmp: CmpIsYes}
	hazYes := Condition{Node: "hazmat", Cmp: CmpIsYes}
	humid := Condition{Node: "cold_storage", Field: "humidity", Cmp: CmpLte, Value: 60.0}
	tcYes := Condition{Node: "temp_control", Cmp: CmpIsYes}
	s := fixtureSnap(
		ind("fit", []Condition{coldYes}, nil),
		ind("notfit", []Condition{hazYes}, nil),
		ind("partial", []Condition{coldYes}, []Condition{humid}), // null humidity → F (D-125)
		ind("unverified", []Condition{coldYes}, []Condition{tcYes}),
	)
	a := baseAttrs()
	a["temp_control"] = NodeState{Status: StatusUnknown}
	r := Evaluate(s, a, time.Time{})
	want := map[string]Verdict{"fit": VerdictFit, "notfit": VerdictNotFit, "partial": VerdictPartial, "unverified": VerdictUnverified}
	for k, w := range want {
		if r.Fit[k] != w {
			t.Errorf("%s = %s, want %s", k, r.Fit[k], w)
		}
	}
	if !slices.Contains(r.Projection.Fit, "partial:P") {
		t.Errorf("projection fit = %v", r.Projection.Fit)
	}
}

func TestProjectionAndNeedsInfo(t *testing.T) {
	s := fixtureSnap()
	a := baseAttrs()
	delete(a["cold_storage"].Fields, "temperature") // required missing (D-127)
	a["temp_control"] = NodeState{Status: StatusUnknown}
	a["hazmat"] = NodeState{Status: StatusYes, Fields: map[string]*FieldValue{
		"dg_classes": val([]string{"c3", "c8"}), "owner_consent": val(true),
	}}
	now := time.Unix(1700000000, 0).UTC()
	r := Evaluate(s, a, now)
	p := r.Projection

	wantChips := []string{"cold_storage", "cold_storage.temp_type:frozen", "hazmat", "hazmat.dg_classes:c3", "hazmat.dg_classes:c8", "hazmat.owner_consent"}
	if !slices.Equal(p.Chips, wantChips) {
		t.Errorf("chips = %v, want %v", p.Chips, wantChips)
	}
	wantUnk := []string{"cold_storage.temperature", "temp_control"}
	if !slices.Equal(p.Unk, wantUnk) {
		t.Errorf("unk = %v, want %v", p.Unk, wantUnk)
	}
	keys := []string{}
	for _, n := range p.Nums {
		keys = append(keys, n.K)
	}
	wantNums := []string{"warehouse.total_area", "infrastructure.dock_doors", "infrastructure.dock_ratio"}
	if !slices.Equal(keys, wantNums) {
		t.Errorf("nums = %v, want %v", keys, wantNums)
	}
	wantNeeds := []string{"temp_control", "warehouse.name", "warehouse.address", "warehouse.location", "warehouse.rent", "cold_storage.temperature"}
	if !slices.Equal(p.NeedsInfo, wantNeeds) || p.NeedsInfoCount != len(wantNeeds) {
		t.Errorf("needsInfo = %v, want %v", p.NeedsInfo, wantNeeds)
	}
	if slices.Contains(p.NeedsInfo, "cold_storage.humidity") {
		t.Error("null optional field must never be in needsInfo (D-139)")
	}
	if p.FitRulesVersion != 7 || !p.EvaluatedAt.Equal(now) {
		t.Errorf("version/time = %d %v", p.FitRulesVersion, p.EvaluatedAt)
	}
}

func TestProjectionHidesNonPublic(t *testing.T) {
	nodes := fixtureNodes()
	nodes[2].Public = false // cold_storage
	s := NewSnapshot(1, nodes, nil)
	r := Evaluate(s, baseAttrs(), time.Time{})
	for _, c := range r.Projection.Chips {
		if c == "cold_storage" || c == "cold_storage.temp_type:frozen" {
			t.Fatalf("non-public node leaked into chips: %v", r.Projection.Chips)
		}
	}
}

func TestEvaluateBSONShapes(t *testing.T) {
	// Values as the Mongo driver decodes them into `any`.
	s := fixtureSnap()
	a := baseAttrs()
	a[RootKey].Fields["total_area"] = val(mustD(map[string]any{"value": 10000.0, "unit": "sqft", "sqm": 929.0304}))
	a["infrastructure"].Fields["dock_doors"] = val(int32(4))
	r := Evaluate(s, a, time.Time{})
	if v := r.Ratios["infrastructure.dock_ratio"]; math.Abs(v-4) > 1e-9 {
		t.Fatalf("ratio from BSON shapes = %v", v)
	}
}
