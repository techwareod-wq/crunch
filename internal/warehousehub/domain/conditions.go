package domain

import (
	"fmt"
	"slices"
)

// Cmp is a condition comparator.
type Cmp string

const (
	CmpEq          Cmp = "eq"           // bool, number, pick
	CmpGte         Cmp = "gte"          // number, calculated, range (max reaches ≥ value)
	CmpLte         Cmp = "lte"          // number, calculated, range (min reaches ≤ value)
	CmpIn          Cmp = "in"           // pick: value is one of; multi: any overlap
	CmpContains    Cmp = "contains"     // multi: holds the value
	CmpContainsAll Cmp = "contains_all" // multi: holds every value (Pharma zone segregation)
)

// Condition is one rule on one attribute: `{attr, cmp, value}`. Used by
// industry rules and appliesWhen.
type Condition struct {
	Attr  string `bson:"attr"  json:"attr"`
	Cmp   Cmp    `bson:"cmp"   json:"cmp"`
	Value any    `bson:"value" json:"value"`
}

// CondOp combines a CondNode's conditions.
type CondOp string

const (
	OpAny CondOp = "any"
	OpAll CondOp = "all"
)

// CondNode is a one-level any/all over conditions (D-032). Stored as a node
// so `{op, conds: [Condition | CondNode]}` nesting later is a superset.
type CondNode struct {
	Op    CondOp      `bson:"op"    json:"op"`
	Conds []Condition `bson:"conds" json:"conds"`
}

// MaxConds bounds one rule list.
const MaxConds = 20

// NormalizeCondition validates c against the tree and returns it with Value
// coerced to its canonical Go type (bool | float64 | string | []string). The
// referenced attribute must exist, be a non-retired attribute, and accept the
// comparator. Number values are canonical units.
func NormalizeCondition(s *Snapshot, c Condition) (Condition, error) {
	d, ok := s.Def(c.Attr)
	if !ok {
		return c, fmt.Errorf("condition references unknown attribute %q", c.Attr)
	}
	if d.IsGroup() {
		return c, fmt.Errorf("condition references group %q", c.Attr)
	}
	if d.Retired {
		return c, fmt.Errorf("condition references retired attribute %q", c.Attr)
	}
	bad := func() (Condition, error) {
		return c, fmt.Errorf("condition on %q: comparator %q with value %v is not valid for type %s", c.Attr, c.Cmp, c.Value, d.Type)
	}
	switch d.Type {
	case TypeBool:
		b, ok := asBool(c.Value)
		if c.Cmp != CmpEq || !ok {
			return bad()
		}
		c.Value = b
	case TypeNumber, TypeCalculated:
		f, ok := asFloat(c.Value)
		if !ok || (c.Cmp != CmpEq && c.Cmp != CmpGte && c.Cmp != CmpLte) {
			return bad()
		}
		c.Value = f
	case TypeRange:
		f, ok := asFloat(c.Value)
		if !ok || (c.Cmp != CmpGte && c.Cmp != CmpLte) {
			return bad()
		}
		c.Value = f
	case TypePick:
		switch c.Cmp {
		case CmpEq:
			v, ok := asString(c.Value)
			if !ok || !d.HasAllowedValue(v) {
				return bad()
			}
			c.Value = v
		case CmpIn:
			vs, ok := asStrings(c.Value)
			if !ok || !validOptions(d, vs) {
				return bad()
			}
			c.Value = vs
		default:
			return bad()
		}
	case TypeMulti:
		switch c.Cmp {
		case CmpContains:
			v, ok := asString(c.Value)
			if !ok || !d.HasAllowedValue(v) {
				return bad()
			}
			c.Value = v
		case CmpContainsAll, CmpIn:
			vs, ok := asStrings(c.Value)
			if !ok || !validOptions(d, vs) {
				return bad()
			}
			c.Value = vs
		default:
			return bad()
		}
	default: // text, money: not rule material
		return bad()
	}
	return c, nil
}

func validOptions(d *AttrDef, vs []string) bool {
	if len(vs) == 0 {
		return false
	}
	for _, v := range vs {
		if !d.HasAllowedValue(v) {
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

// compare evaluates a known answer value against c. A value of the wrong
// shape is "not met" (F): it can only come from a corrupted doc, and failing
// closed keeps a bad row out of fit results.
func compare(t AttrType, v any, c Condition) bool {
	switch t {
	case TypeBool:
		a, ok1 := asBool(v)
		b, ok2 := asBool(c.Value)
		return ok1 && ok2 && a == b
	case TypeNumber, TypeCalculated:
		a, ok1 := asFloat(v)
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
