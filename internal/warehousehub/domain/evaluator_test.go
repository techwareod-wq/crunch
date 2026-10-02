package domain

import (
	"math"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// fixture: a cut-down tree mirroring the seed.
func fixtureDefs() []AttrDef {
	attr := func(key, parent string, t AttrType, order int) AttrDef {
		return AttrDef{Key: key, Kind: KindAttribute, Type: t, Name: key, ParentKey: parent, Order: order, Filterable: true, FilterRow: "facility", Public: true}
	}
	group := func(key string, order int) AttrDef {
		return AttrDef{Key: key, Kind: KindGroup, Name: key, Order: order}
	}
	tempTracking := attr("temp_tracking", "cold_storage", TypeBool, 2)
	tempTracking.AppliesWhen = &CondNode{Op: OpAny, Conds: []Condition{{Attr: "gdp_compliant", Cmp: CmpEq, Value: true}}}
	tempRange := attr("temp_range", "cold_storage", TypeRange, 1)
	tempRange.Unit = &UnitSpec{Dimension: DimTemp, Input: []string{"C", "F"}}
	zones := attr("zone_segregation", "storage", TypeMulti, 5)
	zones.AllowedValues = []AllowedValue{{Key: "quarantine", Label: "Q"}, {Key: "received", Label: "R"}, {Key: "waste", Label: "W"}, {Key: "returns", Label: "Ret"}}
	tempType := attr("temp_type", "storage", TypePick, 6)
	tempType.AllowedValues = []AllowedValue{{Key: "ambient", Label: "A"}, {Key: "chilled", Label: "C"}, {Key: "frozen", Label: "F"}}
	doors := attr("dock_doors", "infra", TypeNumber, 1)
	doors.Unit = &UnitSpec{Dimension: DimCount}
	ratio := attr("dock_ratio", "dock_doors", TypeCalculated, 1)
	ratio.Calc = &CalcSpec{Fn: "dock_ratio"}
	floor := attr("floor_strength", "infra", TypeNumber, 2)
	floor.Unit = &UnitSpec{Dimension: DimLoad}
	licence := attr("bonded_licence_no", "bonded", TypeText, 1)
	licence.Filterable, licence.Public = false, false
	old := attr("old_attr", "storage", TypeBool, 9)
	old.Retired = true

	return []AttrDef{
		group("storage", 1), group("infra", 2), group("compliance", 3),
		attr("cold_storage", "storage", TypeBool, 1),
		tempRange, tempTracking,
		attr("deep_freeze", "cold_storage", TypeBool, 3),
		attr("racked_storage", "storage", TypeBool, 2),
		zones, tempType, old,
		doors, ratio, floor,
		attr("power_backup", "infra", TypeBool, 3),
		attr("gdp_compliant", "compliance", TypeBool, 1),
		attr("fire_noc", "compliance", TypeBool, 2),
		attr("bonded", "compliance", TypeBool, 3),
		licence,
	}
}

func fixtureIndustries() []Industry {
	return []Industry{
		{Key: "pharma", Order: 1, Required: []Condition{
			{Attr: "gdp_compliant", Cmp: CmpEq, Value: true},
			{Attr: "cold_storage", Cmp: CmpEq, Value: true},
			{Attr: "zone_segregation", Cmp: CmpContainsAll, Value: []string{"quarantine", "received", "waste"}},
		}},
		{Key: "fmcg", Order: 2, Preferred: []Condition{
			{Attr: "racked_storage", Cmp: CmpEq, Value: true},
			{Attr: "dock_ratio", Cmp: CmpGte, Value: 1.0},
		}},
		{Key: "heavy", Order: 3, Required: []Condition{{Attr: "floor_strength", Cmp: CmpGte, Value: 5.0}}},
		{Key: "textiles", Order: 4},
		{Key: "gone", Order: 5, Retired: true},
	}
}

func snap() *Snapshot { return NewSnapshot(7, fixtureDefs(), fixtureIndustries()) }

func yes() Answer          { return Answer{Status: StatusKnown, V: true} }
func no() Answer           { return Answer{Status: StatusKnown, V: false} }
func unk() Answer          { return Answer{Status: StatusUnknown} }
func na() Answer           { return Answer{Status: StatusNA} }
func num(f float64) Answer { return Answer{Status: StatusKnown, V: f} }

func eval(attrs map[string]Answer, areaSqm float64) Result {
	return Evaluate(snap(), EvalInput{Attributes: attrs, TotalAreaSqm: areaSqm}, time.Unix(0, 0))
}

func TestVerdicts(t *testing.T) {
	pharmaOK := map[string]Answer{
		"gdp_compliant": yes(), "cold_storage": yes(),
		"zone_segregation": {Status: StatusKnown, V: []string{"quarantine", "received", "waste", "returns"}},
	}
	with := func(base map[string]Answer, k string, a Answer) map[string]Answer {
		out := map[string]Answer{}
		for kk, v := range base {
			out[kk] = v
		}
		out[k] = a
		return out
	}
	cases := []struct {
		name  string
		attrs map[string]Answer
		area  float64
		ind   string
		want  Verdict
	}{
		{"all required met, no preferred → fit", pharmaOK, 0, "pharma", VerdictFit},
		{"required false → not fit", with(pharmaOK, "cold_storage", no()), 0, "pharma", VerdictNotFit},
		{"required unknown → unverified", with(pharmaOK, "gdp_compliant", unk()), 0, "pharma", VerdictUnverified},
		{"required absent → unverified", map[string]Answer{"gdp_compliant": yes(), "cold_storage": yes()}, 0, "pharma", VerdictUnverified},
		{"N/A on required → not fit (D-038)", with(pharmaOK, "gdp_compliant", na()), 0, "pharma", VerdictNotFit},
		{"contains_all missing one → not fit", with(pharmaOK, "zone_segregation", Answer{Status: StatusKnown, V: []string{"quarantine", "waste"}}), 0, "pharma", VerdictNotFit},
		{"required F beats unknown → not fit", map[string]Answer{"cold_storage": no()}, 0, "pharma", VerdictNotFit},
		// fmcg: no required; preferred racked + dock ratio
		{"preferred all true → fit", map[string]Answer{"racked_storage": yes(), "dock_doors": num(10)}, 50000 * SqmPerSqft, "fmcg", VerdictFit},
		{"preferred some false → partial", map[string]Answer{"racked_storage": no(), "dock_doors": num(10)}, 50000 * SqmPerSqft, "fmcg", VerdictPartial},
		{"preferred unknown → unverified (D-033)", map[string]Answer{"racked_storage": yes()}, 50000 * SqmPerSqft, "fmcg", VerdictUnverified},
		{"dock ratio unknown without area → unverified", map[string]Answer{"racked_storage": yes(), "dock_doors": num(10)}, 0, "fmcg", VerdictUnverified},
		{"no rules → fit", nil, 0, "textiles", VerdictFit},
		{"gte threshold met", map[string]Answer{"floor_strength": num(5)}, 0, "heavy", VerdictFit},
		{"gte threshold missed", map[string]Answer{"floor_strength": num(4.9)}, 0, "heavy", VerdictNotFit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eval(tc.attrs, tc.area).Fit[tc.ind]
			if got != tc.want {
				t.Fatalf("verdict %s = %s, want %s", tc.ind, got, tc.want)
			}
		})
	}
}

func TestRetiredIndustryNotEvaluated(t *testing.T) {
	r := eval(nil, 0)
	if _, ok := r.Fit["gone"]; ok {
		t.Fatal("retired industry got a verdict")
	}
	if slices.ContainsFunc(r.Projection.Fit, func(s string) bool { return s[:4] == "gone" }) {
		t.Fatal("retired industry in projection")
	}
}

// Attribute Tree doc §3 table: temperature tracking = cold storage OR GDP.
func TestAppliesWhenOrAcrossTwoParents(t *testing.T) {
	cases := []struct {
		cold, gdp Answer
		want      Tri
	}{
		{yes(), no(), TriTrue},
		{no(), yes(), TriTrue},
		{yes(), yes(), TriTrue},
		{no(), no(), TriFalse},
		{unk(), no(), TriUnknown},
		{unk(), yes(), TriTrue},
	}
	for i, tc := range cases {
		r := eval(map[string]Answer{"cold_storage": tc.cold, "gdp_compliant": tc.gdp}, 0)
		if got := r.Applicable["temp_tracking"]; got != tc.want {
			t.Errorf("case %d: applicable(temp_tracking) = %d, want %d", i, got, tc.want)
		}
	}
}

func TestAppliesWhenAll(t *testing.T) {
	s := fixtureDefs()
	for i := range s {
		if s[i].Key == "deep_freeze" {
			s[i].AppliesWhen = &CondNode{Op: OpAll, Conds: []Condition{{Attr: "fire_noc", Cmp: CmpEq, Value: true}}}
		}
	}
	sn := NewSnapshot(1, s, nil)
	app := func(cold, noc Answer) Tri {
		return Evaluate(sn, EvalInput{Attributes: map[string]Answer{"cold_storage": cold, "fire_noc": noc}}, time.Time{}).Applicable["deep_freeze"]
	}
	if app(yes(), yes()) != TriTrue || app(yes(), no()) != TriFalse || app(no(), yes()) != TriFalse || app(yes(), unk()) != TriUnknown {
		t.Fatal("all-node applicability wrong")
	}
}

func TestNeedsInfoUnknownParentFlaggedFirst(t *testing.T) {
	r := eval(map[string]Answer{"gdp_compliant": no()}, 0)
	if !slices.Contains(r.NeedsInfo, "cold_storage") {
		t.Fatal("unknown parent not flagged")
	}
	for _, child := range []string{"deep_freeze", "temp_range", "temp_tracking"} {
		if slices.Contains(r.NeedsInfo, child) {
			t.Fatalf("child %s flagged while its parent is unknown", child)
		}
	}
	// Parent yes → children flagged; N/A and known never flagged.
	r = eval(map[string]Answer{"cold_storage": yes(), "deep_freeze": na(), "temp_range": {Status: StatusKnown, V: Range{-20, -5}}}, 0)
	if !slices.Contains(r.NeedsInfo, "temp_tracking") {
		t.Fatal("applicable child not flagged")
	}
	if slices.Contains(r.NeedsInfo, "deep_freeze") || slices.Contains(r.NeedsInfo, "temp_range") {
		t.Fatal("na/known answer flagged")
	}
	// Parent switched to no: children ignored even though answered.
	r = eval(map[string]Answer{"cold_storage": no(), "deep_freeze": yes()}, 0)
	if r.IsApplicable("deep_freeze") || slices.Contains(r.Projection.Chips, "deep_freeze:yes") {
		t.Fatal("child of a 'no' parent still applies")
	}
}

func TestNeedsInfoSkipsCalculatedRetiredAndGroups(t *testing.T) {
	r := eval(nil, 0)
	for _, k := range []string{"dock_ratio", "old_attr", "storage", "infra"} {
		if slices.Contains(r.NeedsInfo, k) {
			t.Fatalf("%s flagged", k)
		}
	}
	if r.Projection.NeedsInfoCount != len(r.NeedsInfo) {
		t.Fatal("count mismatch")
	}
}

func TestRetiredAttrConditionIsFalse(t *testing.T) {
	sn := NewSnapshot(1, fixtureDefs(), []Industry{{Key: "x", Required: []Condition{{Attr: "old_attr", Cmp: CmpEq, Value: true}}}})
	r := Evaluate(sn, EvalInput{Attributes: map[string]Answer{"old_attr": yes()}}, time.Time{})
	if r.Fit["x"] != VerdictNotFit {
		t.Fatalf("rule on retired attr = %s, want N", r.Fit["x"])
	}
}

func TestDockRatio(t *testing.T) {
	r := eval(map[string]Answer{"dock_doors": num(10)}, 50000*SqmPerSqft)
	if got := r.Calc["dock_ratio"]; math.Abs(got-2.0) > 1e-9 {
		t.Fatalf("dock_ratio = %v, want 2", got)
	}
	if _, ok := eval(map[string]Answer{"dock_doors": num(10)}, 0).Calc["dock_ratio"]; ok {
		t.Fatal("dock ratio computed without total area")
	}
	if _, ok := eval(map[string]Answer{"dock_doors": unk()}, 1000).Calc["dock_ratio"]; ok {
		t.Fatal("dock ratio computed with unknown doors")
	}
	// A stored value under a calculated key is ignored.
	if _, ok := eval(map[string]Answer{"dock_ratio": num(99)}, 0).Calc["dock_ratio"]; ok {
		t.Fatal("stored calculated value used")
	}
}

func TestProjection(t *testing.T) {
	r := eval(map[string]Answer{
		"cold_storage":      yes(),
		"temp_range":        {Status: StatusKnown, V: Range{-20, -5}},
		"racked_storage":    no(),
		"zone_segregation":  {Status: StatusKnown, V: []string{"quarantine", "waste"}},
		"temp_type":         {Status: StatusKnown, V: "frozen"},
		"dock_doors":        num(4),
		"floor_strength":    na(),
		"bonded_licence_no": {Status: StatusKnown, V: "MUM-1"},
	}, 20000*SqmPerSqft)
	p := r.Projection
	for _, c := range []string{"cold_storage:yes", "zone_segregation:quarantine", "zone_segregation:waste", "temp_type:frozen"} {
		if !slices.Contains(p.Chips, c) {
			t.Errorf("missing chip %s in %v", c, p.Chips)
		}
	}
	if slices.Contains(p.Chips, "racked_storage:yes") {
		t.Error("false bool emitted a chip")
	}
	wantNums := map[string]float64{"temp_range_min": -20, "temp_range_max": -5, "dock_doors": 4, "dock_ratio": 2}
	for k, v := range wantNums {
		if !slices.ContainsFunc(p.Nums, func(n NumFact) bool { return n.K == k && math.Abs(n.V-v) < 1e-9 }) {
			t.Errorf("missing num %s=%v in %v", k, v, p.Nums)
		}
	}
	// unk: applicable + unanswered (deep_freeze, temp_tracking, gdp…), never
	// na (floor_strength) or non-public (licence).
	for _, k := range []string{"deep_freeze", "temp_tracking", "gdp_compliant"} {
		if !slices.Contains(p.Unk, k) {
			t.Errorf("missing unk %s in %v", k, p.Unk)
		}
	}
	for _, k := range []string{"floor_strength", "bonded_licence_no", "cold_storage", "old_attr"} {
		if slices.Contains(p.Unk, k) {
			t.Errorf("unexpected unk %s", k)
		}
	}
	if slices.ContainsFunc(p.Chips, func(c string) bool { return len(c) > 7 && c[:7] == "bonded_" }) {
		t.Error("non-public attr leaked into chips")
	}
	if p.FitRulesVersion != 7 || len(p.Fit) != 4 {
		t.Errorf("fit projection = %v @ %d", p.Fit, p.FitRulesVersion)
	}
}

// Answers read back from Mongo arrive as BSON-generic types.
func TestEvaluateAcceptsBSONShapes(t *testing.T) {
	r := eval(map[string]Answer{
		"gdp_compliant":    yes(),
		"cold_storage":     yes(),
		"zone_segregation": {Status: StatusKnown, V: primitive.A{"quarantine", "received", "waste"}},
		"temp_range":       {Status: StatusKnown, V: primitive.D{{Key: "min", Value: int32(-20)}, {Key: "max", Value: -2.5}}},
		"floor_strength":   {Status: StatusKnown, V: int64(6)},
	}, 0)
	if r.Fit["pharma"] != VerdictFit || r.Fit["heavy"] != VerdictFit {
		t.Fatalf("fit = %v", r.Fit)
	}
	if !slices.ContainsFunc(r.Projection.Nums, func(n NumFact) bool { return n.K == "temp_range_min" && n.V == -20 }) {
		t.Fatal("bson range not projected")
	}
}

func TestCanonicalizeAnswer(t *testing.T) {
	s := snap()
	d := func(k string) *AttrDef { x, _ := s.Def(k); return x }

	a, err := CanonicalizeAnswer(d("temp_range"), Answer{Status: StatusKnown, Raw: &RawValue{Value: map[string]any{"min": 32.0, "max": 50.0}, Unit: "F"}})
	if err != nil || a.V.(Range).Min != 0 || a.V.(Range).Max != 10 {
		t.Fatalf("F→C range: %v %v", a.V, err)
	}
	a, err = CanonicalizeAnswer(d("floor_strength"), Answer{Status: StatusKnown, V: 5.0})
	if err != nil || a.V != 5.0 || a.Raw.Unit != "t/m2" {
		t.Fatalf("number: %+v %v", a, err)
	}
	if _, err := CanonicalizeAnswer(d("floor_strength"), Answer{Status: StatusKnown, Raw: &RawValue{Value: 5.0, Unit: "sqft"}}); err == nil {
		t.Fatal("wrong unit accepted")
	}
	if _, err := CanonicalizeAnswer(d("temp_type"), Answer{Status: StatusKnown, V: "lukewarm"}); err == nil {
		t.Fatal("unknown pick value accepted")
	}
	a, _ = CanonicalizeAnswer(d("zone_segregation"), Answer{Status: StatusKnown, V: []any{"waste", "waste", "received"}})
	if got := a.V.([]string); len(got) != 2 {
		t.Fatalf("multi not deduped: %v", got)
	}
	if _, err := CanonicalizeAnswer(d("dock_ratio"), Answer{Status: StatusKnown, V: 1.0}); err == nil {
		t.Fatal("calculated answer accepted")
	}
	a, err = CanonicalizeAnswer(d("cold_storage"), Answer{Status: StatusNA, V: true})
	if err != nil || a.V != nil {
		t.Fatal("na kept a value")
	}
	if _, err := CanonicalizeAnswer(d("cold_storage"), Answer{Status: "maybe"}); err == nil {
		t.Fatal("bad status accepted")
	}
}

func TestUnits(t *testing.T) {
	if v, _ := ToCanonical(DimArea, 10000, UnitSqft); math.Abs(v-929.0304) > 1e-9 {
		t.Fatalf("sqft→sqm = %v", v)
	}
	if v, _ := ToCanonical(DimLength, 30, UnitFt); math.Abs(v-9.144) > 1e-9 {
		t.Fatalf("ft→m = %v", v)
	}
	if v, _ := ToCanonical(DimTemp, -4, UnitF); math.Abs(v+20) > 1e-9 {
		t.Fatalf("F→C = %v", v)
	}
	if _, err := ToCanonical(DimMass, 1, "kg"); err == nil {
		t.Fatal("kg accepted")
	}
}
