package domain

import (
	"strings"
	"testing"
)

func TestValidateNode(t *testing.T) {
	s := fixtureSnap()
	ok := Node{Key: "racking", ParentKey: RootKey, Name: " Racking ", Fields: []Field{
		{Key: "levels", Name: "Levels", Type: TypeNumber},
		{Key: "per_level", Name: "Per level", Type: TypeRatio, Ratio: &RatioSpec{Top: "racking.levels", Bottom: "warehouse.total_area", Per: 1000}},
	}}
	n, err := ValidateNode(s, ok)
	if err != nil || n.Name != "Racking" {
		t.Fatalf("valid node: %v %+v", err, n)
	}

	bad := []struct {
		name string
		edit func(*Node)
		msg  string
	}{
		{"bad key", func(n *Node) { n.Key = "Racking" }, "key"},
		{"no parent", func(n *Node) { n.ParentKey = "" }, "parentKey"},
		{"missing parent", func(n *Node) { n.ParentKey = "ghost" }, "does not exist"},
		{"dup field", func(n *Node) { n.Fields = append(n.Fields, n.Fields[0]) }, "duplicate"},
		{"unit on text", func(n *Node) {
			n.Fields[0] = Field{Key: "x", Name: "x", Type: TypeText, Unit: &UnitSpec{Family: DimArea}}
		}, "unit"},
		{"pick without options", func(n *Node) { n.Fields[0] = Field{Key: "x", Name: "x", Type: TypePick} }, "options"},
		{"required ratio", func(n *Node) { n.Fields[1].Required = true }, "can't be required"},
		{"ratio on text", func(n *Node) {
			n.Fields[0].Type = TypeText
		}, "number or area"},
		{"ratio bad unit", func(n *Node) { n.Fields[1].Ratio.BottomUnit = "ft" }, "does not fit"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			n := ok.Clone()
			c.edit(&n)
			_, err := ValidateNode(s, n)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want it to mention %q", err, c.msg)
			}
		})
	}
}

func TestMoveCycle(t *testing.T) {
	s := fixtureSnap()
	n, _ := s.Node("cold_storage")
	m := n.Clone()
	m.ParentKey = "temp_control" // its own child
	if _, err := ValidateNode(s, m); err == nil {
		t.Fatal("cycle accepted")
	}
	m.ParentKey = "cold_storage"
	if _, err := ValidateNode(s, m); err == nil {
		t.Fatal("self-parent accepted")
	}
}

func TestCheckFieldUpdate(t *testing.T) {
	root := RootNode()
	name, _ := root.Field("name")
	upd := name.Clone()
	upd.Name, upd.Description, upd.Order = "Listing name", "shown as the title", 9
	if err := CheckFieldUpdate(*name, upd); err != nil {
		t.Fatalf("rename of a locked field refused: %v", err)
	}
	upd.Required = false
	if err := CheckFieldUpdate(*name, upd); err == nil {
		t.Fatal("locked field made optional")
	}

	f := Field{Key: "t", Type: TypePick, Options: []Option{{Key: "a"}, {Key: "b"}}}
	g := f.Clone()
	g.Type = TypeMulti
	if err := CheckFieldUpdate(f, g); err == nil {
		t.Fatal("type change accepted (D-130)")
	}
	g = f.Clone()
	g.Options = g.Options[:1]
	if err := CheckFieldUpdate(f, g); err == nil {
		t.Fatal("option removed through update (D-138)")
	}
	g = f.Clone()
	g.Options = append(g.Options, Option{Key: "c"})
	g.Required = true
	if err := CheckFieldUpdate(f, g); err != nil {
		t.Fatalf("adding an option / making required refused: %v", err)
	}
}

func TestValidateIndustry(t *testing.T) {
	s := fixtureSnap()
	ind, err := ValidateIndustry(s, Industry{Key: "pharma", Name: "Pharma",
		Required:  []Condition{{Node: "cold_storage", Cmp: CmpIsYes}},
		Preferred: []Condition{{Node: "cold_storage", Field: "temp_type", Cmp: CmpIn, Value: []any{"chilled", "chilled"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if vs := ind.Preferred[0].Value.([]string); len(vs) != 1 {
		t.Errorf("values not deduped: %v", vs)
	}
	for _, c := range []Condition{
		{Node: "ghost", Cmp: CmpIsYes},
		{Node: "cold_storage", Field: "temperature", Cmp: CmpIsYes},
		{Node: "cold_storage", Field: "temp_type", Cmp: CmpEq, Value: "warm"},
		{Node: "cold_storage", Field: "temperature", Cmp: CmpIn, Value: 3.0},
		{Node: RootKey, Field: "name", Cmp: CmpEq, Value: "x"},
	} {
		if _, err := ValidateIndustry(s, Industry{Key: "x", Name: "x", Required: []Condition{c}}); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if _, err := ValidateIndustry(s, Industry{Key: "x", Name: "x"}); err == nil {
		t.Error("industry without rules accepted")
	}
}

func TestIndustryReferences(t *testing.T) {
	ind := Industry{Required: []Condition{{Node: "cold_storage", Field: "temp_type", Cmp: CmpIn, Value: []string{"frozen"}}}}
	if !ind.References("cold_storage", "", "") || !ind.References("cold_storage", "temp_type", "frozen") {
		t.Error("missed a reference")
	}
	if ind.References("cold_storage", "temp_type", "chilled") || ind.References("hazmat", "", "") {
		t.Error("false reference")
	}
}

func TestRootNodeValid(t *testing.T) {
	if _, err := ValidateNode(EmptySnapshot(), RootNode()); err != nil {
		t.Fatalf("RootNode invalid: %v", err)
	}
}
