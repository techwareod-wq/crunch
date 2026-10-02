package domain

import (
	"strings"
	"testing"
)

func TestValidateDef(t *testing.T) {
	s := snap()
	base := func() AttrDef {
		return AttrDef{Key: "new_attr", Kind: KindAttribute, Type: TypeBool, Name: "New", ParentKey: "storage"}
	}
	cases := []struct {
		name    string
		mut     func(d *AttrDef)
		wantErr string
	}{
		{"ok under group", func(d *AttrDef) {}, ""},
		{"ok under bool", func(d *AttrDef) { d.ParentKey = "cold_storage" }, ""},
		{"ok under pick", func(d *AttrDef) { d.ParentKey = "temp_type" }, ""},
		{"bad key", func(d *AttrDef) { d.Key = "Bad-Key" }, "key must match"},
		{"missing parent", func(d *AttrDef) { d.ParentKey = "nope" }, "does not exist"},
		{"parent is number", func(d *AttrDef) { d.ParentKey = "floor_strength" }, "must be a group or a bool/pick"},
		{"parent is multi", func(d *AttrDef) { d.ParentKey = "zone_segregation" }, "must be a group or a bool/pick"},
		{"calculated may sit under number", func(d *AttrDef) {
			d.ParentKey, d.Type, d.Calc = "dock_doors", TypeCalculated, &CalcSpec{Fn: "dock_ratio"}
		}, ""},
		{"unknown calc fn", func(d *AttrDef) { d.Type, d.Calc = TypeCalculated, &CalcSpec{Fn: "magic"} }, "known calc fn"},
		{"retired parent", func(d *AttrDef) { d.ParentKey = "old_attr" }, "retired"},
		{"number without unit", func(d *AttrDef) { d.Type = TypeNumber }, "needs a unit"},
		{"bad unit for dimension", func(d *AttrDef) {
			d.Type, d.Unit = TypeNumber, &UnitSpec{Dimension: DimArea, Input: []string{"ft"}}
		}, "not valid for area"},
		{"pick without options", func(d *AttrDef) { d.Type = TypePick }, "allowed values"},
		{"duplicate option", func(d *AttrDef) {
			d.Type, d.AllowedValues = TypePick, []AllowedValue{{Key: "a", Label: "A"}, {Key: "a", Label: "B"}}
		}, "duplicated"},
		{"filterable text", func(d *AttrDef) { d.Type, d.Filterable, d.FilterRow = TypeText, true, "x" }, "cannot be filterable"},
		{"filterable needs row", func(d *AttrDef) { d.Filterable = true }, "filterRow"},
		{"group with type", func(d *AttrDef) { d.Kind, d.ParentKey = KindGroup, "" }, "group has no type"},
		{"group under attribute", func(d *AttrDef) { d.Kind, d.Type, d.ParentKey = KindGroup, "", "cold_storage" }, "only sit under another group"},
		{"appliesWhen self", func(d *AttrDef) {
			d.AppliesWhen = &CondNode{Op: OpAny, Conds: []Condition{{Attr: "new_attr", Cmp: CmpEq, Value: true}}}
		}, "itself"},
		{"appliesWhen retired", func(d *AttrDef) {
			d.AppliesWhen = &CondNode{Op: OpAny, Conds: []Condition{{Attr: "old_attr", Cmp: CmpEq, Value: true}}}
		}, "retired attribute"},
		{"appliesWhen bad cmp", func(d *AttrDef) {
			d.AppliesWhen = &CondNode{Op: OpAll, Conds: []Condition{{Attr: "cold_storage", Cmp: CmpGte, Value: 1}}}
		}, "not valid for type bool"},
		{"appliesWhen bad op", func(d *AttrDef) {
			d.AppliesWhen = &CondNode{Op: "xor", Conds: []Condition{{Attr: "cold_storage", Cmp: CmpEq, Value: true}}}
		}, "op must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.mut(&d)
			_, err := ValidateDef(s, d)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDefMoveCycle(t *testing.T) {
	s := snap()
	cs, _ := s.Def("cold_storage")
	moved := *cs
	moved.ParentKey = "deep_freeze" // under its own child
	if _, err := ValidateDef(s, moved); err == nil || !strings.Contains(err.Error(), "under itself") {
		t.Fatalf("cycle move: %v", err)
	}
	moved.ParentKey = "cold_storage"
	if _, err := ValidateDef(s, moved); err == nil {
		t.Fatal("self-parent accepted")
	}
}

func TestValidateDefApplicabilityCycle(t *testing.T) {
	s := snap()
	cs, _ := s.Def("cold_storage")
	upd := *cs
	// cold_storage applies when deep_freeze (its own child) is yes → cycle.
	upd.AppliesWhen = &CondNode{Op: OpAll, Conds: []Condition{{Attr: "deep_freeze", Cmp: CmpEq, Value: true}}}
	if _, err := ValidateDef(s, upd); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("applicability cycle: %v", err)
	}
}

func TestValidateDefNormalizesConditionValues(t *testing.T) {
	d := AttrDef{Key: "x_attr", Kind: KindAttribute, Type: TypeBool, Name: "X", ParentKey: "storage",
		AppliesWhen: &CondNode{Op: OpAny, Conds: []Condition{
			{Attr: "floor_strength", Cmp: CmpGte, Value: int32(5)},
			{Attr: "zone_segregation", Cmp: CmpContainsAll, Value: []any{"waste"}},
		}}}
	out, err := ValidateDef(snap(), d)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.AppliesWhen.Conds[0].Value.(float64); !ok {
		t.Fatalf("number not coerced: %T", out.AppliesWhen.Conds[0].Value)
	}
	if _, ok := out.AppliesWhen.Conds[1].Value.([]string); !ok {
		t.Fatalf("list not coerced: %T", out.AppliesWhen.Conds[1].Value)
	}
}

func TestCheckDefUpdate(t *testing.T) {
	s := snap()
	tt, _ := s.Def("temp_type")
	upd := *tt
	upd.AllowedValues = upd.AllowedValues[:2]
	if err := CheckDefUpdate(*tt, upd); err == nil {
		t.Fatal("removing an allowed value accepted")
	}
	upd = *tt
	upd.Type = TypeMulti
	if err := CheckDefUpdate(*tt, upd); err == nil {
		t.Fatal("type change accepted")
	}
	fs, _ := s.Def("floor_strength")
	upd = *fs
	upd.Unit = &UnitSpec{Dimension: DimMass}
	if err := CheckDefUpdate(*fs, upd); err == nil {
		t.Fatal("dimension change accepted")
	}
	upd = *fs
	upd.Name = "Floor load"
	if err := CheckDefUpdate(*fs, upd); err != nil {
		t.Fatal(err)
	}
}

func TestValidateIndustry(t *testing.T) {
	s := snap()
	ok := Industry{Key: "pharma2", Name: "Pharma", Required: []Condition{{Attr: "cold_storage", Cmp: CmpEq, Value: true}}}
	if _, err := ValidateIndustry(s, ok); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Preferred = []Condition{{Attr: "bonded_licence_no", Cmp: CmpEq, Value: "x"}}
	if _, err := ValidateIndustry(s, bad); err == nil {
		t.Fatal("text rule accepted")
	}
	bad = ok
	bad.Required = []Condition{{Attr: "storage", Cmp: CmpEq, Value: true}}
	if _, err := ValidateIndustry(s, bad); err == nil {
		t.Fatal("group rule accepted")
	}
	bad = ok
	bad.Required = []Condition{{Attr: "temp_type", Cmp: CmpEq, Value: "tepid"}}
	if _, err := ValidateIndustry(s, bad); err == nil {
		t.Fatal("unknown option accepted")
	}
}

func TestSnapshotOrderAndDescendants(t *testing.T) {
	s := snap()
	if got := s.Children(""); len(got) != 3 || got[0] != "storage" {
		t.Fatalf("roots = %v", got)
	}
	d := s.Descendants("cold_storage")
	if len(d) != 3 {
		t.Fatalf("descendants = %v", d)
	}
	if !s.IsAncestor("storage", "deep_freeze") || s.IsAncestor("deep_freeze", "storage") {
		t.Fatal("IsAncestor wrong")
	}
	// Tree order is depth-first.
	idx := map[string]int{}
	for i, x := range s.Defs {
		idx[x.Key] = i
	}
	if !(idx["storage"] < idx["cold_storage"] && idx["cold_storage"] < idx["temp_range"] && idx["temp_range"] < idx["racked_storage"]) {
		t.Fatal("not depth-first")
	}
}
