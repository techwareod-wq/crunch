package domain

import (
	"encoding/json"
	"math"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Value accessors. Answer and condition values arrive in three shapes: typed
// Go values (built in code), JSON-decoded (float64, []any, map[string]any) and
// BSON-decoded (int32/int64, primitive.A, primitive.D). Each accessor accepts
// all three so callers never care where a value came from.

func asBool(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

func asFloat(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		x, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = x
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asStrings(v any) ([]string, bool) {
	switch l := v.(type) {
	case []string:
		return l, true
	case []any:
		return stringsOf(l)
	case primitive.A:
		return stringsOf(l)
	}
	return nil, false
}

func stringsOf(l []any) ([]string, bool) {
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// asMap flattens the object shapes into a map.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case bson.M:
		return m, true
	case primitive.D:
		return m.Map(), true
	}
	return nil, false
}

func asRange(v any) (Range, bool) {
	switch r := v.(type) {
	case Range:
		return r, true
	case *Range:
		if r == nil {
			return Range{}, false
		}
		return *r, true
	}
	m, ok := asMap(v)
	if !ok {
		return Range{}, false
	}
	lo, ok1 := asFloat(m["min"])
	hi, ok2 := asFloat(m["max"])
	if !ok1 || !ok2 {
		return Range{}, false
	}
	return Range{Min: lo, Max: hi}, true
}

func asMoney(v any) (Money, bool) {
	switch x := v.(type) {
	case Money:
		return x, true
	case *Money:
		if x == nil {
			return Money{}, false
		}
		return *x, true
	}
	m, ok := asMap(v)
	if !ok {
		return Money{}, false
	}
	amt, ok := asFloat(m["amount"])
	if !ok || amt != math.Trunc(amt) {
		return Money{}, false
	}
	cur, ok := m["currency"].(string)
	if !ok {
		return Money{}, false
	}
	basis, _ := m["basis"].(string)
	period, _ := m["period"].(string)
	return Money{Amount: int64(amt), Currency: cur, Basis: basis, Period: period}, true
}
