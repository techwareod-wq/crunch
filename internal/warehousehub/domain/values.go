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

// decodeDoc reads an object-shaped value into T. JSON-decoded maps (API
// input, camelCase keys) go through encoding/json; BSON-decoded documents
// (stored values, snake_case keys) go through bson. Unknown keys are ignored.
func decodeDoc[T any](v any) (T, bool) {
	var out T
	switch x := v.(type) {
	case T:
		return x, true
	case *T:
		if x == nil {
			return out, false
		}
		return *x, true
	case map[string]any:
		b, err := json.Marshal(x)
		if err != nil || json.Unmarshal(b, &out) != nil {
			return out, false
		}
		return out, true
	case bson.M, primitive.D:
		b, err := bson.Marshal(x)
		if err != nil || bson.Unmarshal(b, &out) != nil {
			return out, false
		}
		return out, true
	}
	return out, false
}

// asArea reads an area value; Sqm is the canonical number.
func asArea(v any) (Area, bool) {
	a, ok := decodeDoc[Area](v)
	if !ok || math.IsNaN(a.Sqm) || math.IsInf(a.Sqm, 0) {
		return Area{}, false
	}
	return a, true
}

// DecodeValue reads a canonical field value (typed, JSON- or BSON-decoded)
// into T: string, bool, Area, Money, Address, Location, Range, …
func DecodeValue[T any](v any) (T, bool) {
	return decodeDoc[T](v)
}

// AsStrings reads a list-of-strings value in any of its shapes.
func AsStrings(v any) ([]string, bool) { return asStrings(v) }

// JSONValue converts BSON-decoded documents (primitive.D, which encodes to
// JSON as a list of {Key, Value} pairs) into plain maps, recursively, so a
// value read from Mongo serializes as the object it was stored from.
func JSONValue(v any) any {
	switch x := v.(type) {
	case primitive.D:
		m := make(map[string]any, len(x))
		for _, e := range x {
			m[e.Key] = JSONValue(e.Value)
		}
		return m
	case bson.M:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = JSONValue(e)
		}
		return m
	case primitive.A:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = JSONValue(e)
		}
		return out
	}
	return v
}
