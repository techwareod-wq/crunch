package domain

import (
	"fmt"
	"slices"
)

// Cmp is a condition comparator (D-141).
type Cmp string

const (
	CmpIsYes       Cmp = "is_yes"       // node state (no field)
	CmpEq          Cmp = "eq"           // bool, number, area, ratio, pick
	CmpGte         Cmp = "gte"          // number, area, ratio; range: max ≥ value ("can reach")
	CmpLte         Cmp = "lte"          // number, area, ratio; range: min ≤ value
	CmpIn          Cmp = "in"           // pick: one of; multi: any overlap
	CmpContains    Cmp = "contains"     // multi: holds the value
	CmpContainsAll Cmp = "contains_all" // multi: holds every value
)

// Condition is one industry rule: `node is_yes` or `node.field <cmp> value`.
// Numeric values are in the field's canonical unit (sq m for area).
type Condition struct {
	Node  string `bson:"node"            json:"node"`
	Field string `bson:"field,omitempty" json:"field,omitempty"`
	Cmp   Cmp    `bson:"cmp"             json:"cmp"`
	Value any    `bson:"value,omitempty" json:"value,omitempty"`
}

// Path is the condition's target: "<node>" or "<node>.<field>".
func (c Condition) Path() string {
	if c.Field == "" {
		return c.Node
	}
	return c.Node + "." + c.Field
}

// MaxConds bounds one rule list.
const MaxConds = 20

// NormalizeCondition validates c against the tree and returns it with Value
// coerced to its canonical Go type (nil | bool | float64 | string | []string).
func NormalizeCondition(s *Snapshot, c Condition) (Condition, error) {
	n, ok := s.Node(c.Node)
	if !ok {
		return c, fmt.Errorf("condition references unknown node %q", c.Node)
	}
	if c.Cmp == CmpIsYes {
		if c.Field != "" {
			return c, fmt.Errorf("condition %q: is_yes takes no field", c.Path())
		}
		c.Value = nil
		return c, nil
	}
	if c.Field == "" {
		return c, fmt.Errorf("condition on %q: %q needs a field", c.Node, c.Cmp)
	}
	f, ok := n.Field(c.Field)
	if !ok {
		return c, fmt.Errorf("condition references unknown field %q", c.Path())
	}
	bad := func() (Condition, error) {
		return c, fmt.Errorf("condition on %q: comparator %q with value %v is not valid for a %s field", c.Path(), c.Cmp, c.Value, f.Type)
	}
	switch f.Type {
	case TypeBool:
		b, ok := asBool(c.Value)
		if c.Cmp != CmpEq || !ok {
			return bad()
		}
		c.Value = b
	case TypeNumber, TypeArea, TypeRatio:
		v, ok := asFloat(c.Value)
		if !ok || (c.Cmp != CmpEq && c.Cmp != CmpGte && c.Cmp != CmpLte) {
			return bad()
		}
		c.Value = v
	case TypeRange:
		v, ok := asFloat(c.Value)
		if !ok || (c.Cmp != CmpGte && c.Cmp != CmpLte) {
			return bad()
		}
		c.Value = v
	case TypePick:
		switch c.Cmp {
		case CmpEq:
			v, ok := asString(c.Value)
			if !ok || !f.HasOption(v) {
				return bad()
			}
			c.Value = v
		case CmpIn:
			vs, ok := asStrings(c.Value)
			if !ok || !validOptions(f, vs) {
				return bad()
			}
			c.Value = dedupe(vs)
		default:
			return bad()
		}
	case TypeMulti:
		switch c.Cmp {
		case CmpContains:
			v, ok := asString(c.Value)
			if !ok || !f.HasOption(v) {
				return bad()
			}
			c.Value = v
		case CmpContainsAll, CmpIn:
			vs, ok := asStrings(c.Value)
			if !ok || !validOptions(f, vs) {
				return bad()
			}
			c.Value = dedupe(vs)
		default:
			return bad()
		}
	default: // text, longtext, money, date, address, location: not rule material
		return bad()
	}
	return c, nil
}

func validOptions(f *Field, vs []string) bool {
	if len(vs) == 0 {
		return false
	}
	for _, v := range vs {
		if !f.HasOption(v) {
			return false
		}
	}
	return true
}

// NormalizeConditions validates a rule list (≤ MaxConds).
func NormalizeConditions(s *Snapshot, conds []Condition) ([]Condition, error) {
	if len(conds) > MaxConds {
		return nil, fmt.Errorf("at most %d conditions", MaxConds)
	}
	out := make([]Condition, 0, len(conds))
	for _, c := range conds {
		n, err := NormalizeCondition(s, c)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// References reports whether any of ind's rules names node (field == "") or
// node.field (option == "") or uses option in its value.
func (ind *Industry) References(node, field, option string) bool {
	for _, set := range [][]Condition{ind.Required, ind.Preferred} {
		for _, c := range set {
			if c.Node != node || (field != "" && c.Field != field) {
				continue
			}
			if option == "" {
				return true
			}
			if v, ok := asString(c.Value); ok && v == option {
				return true
			}
			if vs, ok := asStrings(c.Value); ok && slices.Contains(vs, option) {
				return true
			}
		}
	}
	return false
}

// compare evaluates a present canonical value against c. A value of the
// wrong shape is "not met": it can only come from a corrupted doc, and
// failing closed keeps a bad row out of fit results.
func compare(t FieldType, v any, c Condition) bool {
	switch t {
	case TypeBool:
		a, ok1 := asBool(v)
		b, ok2 := asBool(c.Value)
		return ok1 && ok2 && a == b
	case TypeNumber, TypeArea, TypeRatio:
		a, ok1 := numberOf(t, v)
		b, ok2 := asFloat(c.Value)
		if !ok1 || !ok2 {
			return false
		}
		switch c.Cmp {
		case CmpEq:
			return a == b
		case CmpGte:
			return a >= b
		case CmpLte:
			return a <= b
		}
	case TypeRange:
		r, ok1 := asRange(v)
		b, ok2 := asFloat(c.Value)
		if !ok1 || !ok2 {
			return false
		}
		switch c.Cmp {
		case CmpGte:
			return r.Max >= b
		case CmpLte:
			return r.Min <= b
		}
	case TypePick:
		a, ok := asString(v)
		if !ok {
			return false
		}
		switch c.Cmp {
		case CmpEq:
			b, ok := asString(c.Value)
			return ok && a == b
		case CmpIn:
			bs, ok := asStrings(c.Value)
			return ok && slices.Contains(bs, a)
		}
	case TypeMulti:
		have, ok := asStrings(v)
		if !ok {
			return false
		}
		switch c.Cmp {
		case CmpContains:
			b, ok := asString(c.Value)
			return ok && slices.Contains(have, b)
		case CmpContainsAll:
			bs, ok := asStrings(c.Value)
			if !ok {
				return false
			}
			for _, b := range bs {
				if !slices.Contains(have, b) {
					return false
				}
			}
			return true
		case CmpIn:
			bs, ok := asStrings(c.Value)
			if !ok {
				return false
			}
			for _, b := range bs {
				if slices.Contains(have, b) {
					return true
				}
			}
			return false
		}
	}
	return false
}

// numberOf reads the comparable number of a numeric field's value: canonical
// number, area in sq m, ratio as computed.
func numberOf(t FieldType, v any) (float64, bool) {
	if t == TypeArea {
		a, ok := asArea(v)
		return a.Sqm, ok
	}
	return asFloat(v)
}
