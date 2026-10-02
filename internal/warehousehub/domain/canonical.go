package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Text caps (runes). A maxLength validation can only tighten them.
const (
	MaxTextLen     = 500
	MaxLongtextLen = 10000
)

func maxTextLen(t FieldType) int {
	if t == TypeLongtext {
		return MaxLongtextLen
	}
	return MaxTextLen
}

// CanonicalizeValue validates a submitted field value and returns it in
// stored form. Stored V per type:
//
//	bool → bool · number → float64 (canonical unit; Raw keeps the input)
//	range → Range (canonical; Raw keeps the input) · pick → string
//	multi → []string · text/longtext → string · money → Money
//	date → "YYYY-MM-DD" · address → Address · location → Location
//	area → Area (Sqm filled in)
//
// A nil fv is null: allowed on optional fields only (D-124/D-125). Ratio
// fields are computed and take no value (D-134). The field's validations run
// last.
func CanonicalizeValue(f *Field, fv *FieldValue) (*FieldValue, error) {
	if f.Type == TypeRatio {
		return nil, fmt.Errorf("is calculated and takes no value")
	}
	if fv == nil || fv.V == nil {
		if f.Required {
			return nil, fmt.Errorf("is required")
		}
		return nil, nil
	}
	switch fv.Source {
	case "", SourceSiteVisit, SourceOwner, SourceDocument, SourceOther:
	default:
		return nil, fmt.Errorf("unknown source %q", fv.Source)
	}
	out := *fv
	out.Raw = nil
	switch f.Type {
	case TypeBool:
		b, ok := asBool(fv.V)
		if !ok {
			return nil, fmt.Errorf("expects true or false")
		}
		out.V = b
	case TypeNumber:
		in, unit := fv.V, ""
		if fv.Raw != nil {
			in, unit = fv.Raw.Value, fv.Raw.Unit
		}
		n, ok := asFloat(in)
		if !ok {
			return nil, fmt.Errorf("expects a number")
		}
		c, err := toCanon(f, n, unit)
		if err != nil {
			return nil, err
		}
		out.V, out.Raw = c, &RawValue{Value: n, Unit: unitOrCanon(f, unit)}
	case TypeRange:
		in, unit := fv.V, ""
		if fv.Raw != nil {
			in, unit = fv.Raw.Value, fv.Raw.Unit
		}
		r, ok := asRange(in)
		if !ok || r.Min > r.Max {
			return nil, fmt.Errorf("expects {min, max} with min ≤ max")
		}
		lo, err := toCanon(f, r.Min, unit)
		if err != nil {
			return nil, err
		}
		hi, _ := toCanon(f, r.Max, unit)
		out.V, out.Raw = Range{Min: lo, Max: hi}, &RawValue{Value: r, Unit: unitOrCanon(f, unit)}
	case TypePick:
		s, ok := asString(fv.V)
		if !ok || !f.HasOption(s) {
			return nil, fmt.Errorf("expects one of the options")
		}
		out.V = s
	case TypeMulti:
		ss, ok := asStrings(fv.V)
		if !ok {
			return nil, fmt.Errorf("expects a list of options")
		}
		for _, s := range ss {
			if !f.HasOption(s) {
				return nil, fmt.Errorf("%q is not an option", s)
			}
		}
		out.V = dedupe(ss)
	case TypeText, TypeLongtext:
		s, ok := asString(fv.V)
		s = strings.TrimSpace(s)
		if !ok || utf8.RuneCountInString(s) > maxTextLen(f.Type) {
			return nil, fmt.Errorf("expects text of at most %d characters", maxTextLen(f.Type))
		}
		if s == "" { // blank text is "not provided"
			if f.Required {
				return nil, fmt.Errorf("is required")
			}
			return nil, nil
		}
		out.V = s
	case TypeMoney:
		m, ok := decodeDoc[Money](fv.V)
		m.Currency = strings.ToUpper(strings.TrimSpace(m.Currency))
		if !ok || m.Amount < 0 || !isAlpha(m.Currency, 3) {
			return nil, fmt.Errorf("expects {amount (minor units, ≥ 0), currency (ISO 4217)}")
		}
		if m.OnRequest {
			m.Amount = 0
		}
		out.V = m
	case TypeDate:
		s, ok := asString(fv.V)
		if !ok {
			return nil, fmt.Errorf("expects a date YYYY-MM-DD")
		}
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return nil, fmt.Errorf("expects a date YYYY-MM-DD")
		}
		out.V = s
	case TypeAddress:
		a, ok := decodeDoc[Address](fv.V)
		if !ok {
			return nil, fmt.Errorf("expects an address object")
		}
		a = trimAddress(a)
		if a.Line1 == "" || a.City == "" || !isAlpha(a.Country, 2) {
			return nil, fmt.Errorf("expects line1, city and a 2-letter country code")
		}
		out.V = a
	case TypeLocation:
		l, ok := decodeDoc[Location](fv.V)
		if !ok || l.Lat < -90 || l.Lat > 90 || l.Lng < -180 || l.Lng > 180 ||
			math.IsNaN(l.Lat) || math.IsNaN(l.Lng) {
			return nil, fmt.Errorf("expects {lat, lng} within range")
		}
		if l.Source == "" {
			l.Source = LocationManual
		}
		if l.Source != LocationManual && l.Source != LocationGeocoded {
			return nil, fmt.Errorf("source must be %s or %s", LocationManual, LocationGeocoded)
		}
		out.V = l
	case TypeArea:
		a, ok := decodeDoc[Area](fv.V)
		if !ok || a.Value < 0 || math.IsNaN(a.Value) || math.IsInf(a.Value, 0) {
			return nil, fmt.Errorf("expects {value ≥ 0, unit}")
		}
		if a.Unit == "" {
			a.Unit = UnitSqm
		}
		sqm, err := ToCanonical(DimArea, a.Value, a.Unit)
		if err != nil {
			return nil, err
		}
		a.Sqm = sqm
		out.V = a
	default:
		return nil, fmt.Errorf("unsupported type %q", f.Type)
	}
	if err := RunValidations(f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func toCanon(f *Field, v float64, unit string) (float64, error) {
	if f.Unit == nil {
		if unit != "" {
			return 0, fmt.Errorf("takes no unit")
		}
		return v, nil
	}
	return ToCanonical(f.Unit.Family, v, unit)
}

func unitOrCanon(f *Field, unit string) string {
	if unit != "" || f.Unit == nil {
		return unit
	}
	return CanonicalUnit(f.Unit.Family)
}

func trimAddress(a Address) Address {
	for _, p := range []*string{&a.Line1, &a.Line2, &a.Locality, &a.City, &a.Region, &a.PostalCode} {
		*p = strings.TrimSpace(*p)
	}
	a.Country = strings.ToUpper(strings.TrimSpace(a.Country))
	return a
}

// CanonicalizeAttributes validates a warehouse's attribute data against the
// tree and returns it in stored form (draft saves, 03). Per node:
//   - the key must name a node; status yes or unknown ("no" drops the entry,
//     D-133); the root is always yes;
//   - unknown nodes keep no field values (they are ignored while unknown);
//   - field values are canonicalized; a nil/blank optional value is stored
//     as null (D-125). Required fields may be missing on a draft: the submit
//     gate (SubmitProblems) enforces them.
//
// Errors name the path, e.g. "cold_storage.temperature: expects a number".
func CanonicalizeAttributes(s *Snapshot, in Attributes) (Attributes, error) {
	out := Attributes{}
	for nk, st := range in {
		n, ok := s.Node(nk)
		if !ok {
			return nil, fmt.Errorf("%s: unknown node", nk)
		}
		if nk == RootKey {
			st.Status = StatusYes
		}
		switch st.Status {
		case StatusNo:
			continue
		case StatusUnknown:
			out[nk] = NodeState{Status: StatusUnknown}
			continue
		case StatusYes:
		default:
			return nil, fmt.Errorf("%s: status must be yes, no or unknown", nk)
		}
		fields := map[string]*FieldValue{}
		for fk, fv := range st.Fields {
			f, ok := n.Field(fk)
			if !ok {
				return nil, fmt.Errorf("%s.%s: unknown field", nk, fk)
			}
			if f.Type == TypeRatio {
				continue // computed; silently ignore echoes from the form
			}
			draft := *f // never mutate the snapshot
			draft.Required = false
			v, err := CanonicalizeValue(&draft, fv)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", nk, fk, err)
			}
			if v == nil && f.Required {
				continue // missing, not null: required fields never hold null
			}
			fields[fk] = v
		}
		out[nk] = NodeState{Status: StatusYes, Fields: fields}
	}
	if _, ok := out[RootKey]; !ok && len(s.Nodes) > 0 {
		out[RootKey] = NodeState{Status: StatusYes, Fields: map[string]*FieldValue{}}
	}
	return out, nil
}

// SubmitProblems is the submit gate's attribute check (D-123/D-124): a yes
// node whose parent isn't yes, and every required field without a value on a
// node that is effectively yes. Empty = OK to submit.
func SubmitProblems(s *Snapshot, a Attributes) []string {
	var out []string
	eff := effectiveStates(s, a)
	for i := range s.Nodes {
		n := &s.Nodes[i]
		st, stored := a[n.Key]
		if stored && st.Status == StatusYes && n.Key != RootKey && eff[n.Key] != StatusYes {
			out = append(out, fmt.Sprintf("%s: can only be yes while its parent is yes", n.Key))
			continue
		}
		if eff[n.Key] != StatusYes {
			continue
		}
		for j := range n.Fields {
			f := &n.Fields[j]
			if f.Required && f.Type != TypeRatio && st.Fields[f.Key] == nil {
				out = append(out, fmt.Sprintf("%s.%s: is required", n.Key, f.Key))
			}
		}
	}
	return out
}

// effectiveStates resolves every node's state on a warehouse (spec 02
// Evaluator step 1): the stored state, forced to no when any ancestor isn't
// yes; the root is always yes; absent is no (D-133). Nodes unreachable from
// the root are no.
func effectiveStates(s *Snapshot, a Attributes) map[string]NodeStatus {
	eff := make(map[string]NodeStatus, len(s.Nodes))
	for i := range s.Nodes { // tree order: parents before children
		n := &s.Nodes[i]
		switch {
		case n.Key == RootKey:
			eff[n.Key] = StatusYes
		case eff[n.ParentKey] != StatusYes:
			eff[n.Key] = StatusNo
		default:
			switch a[n.Key].Status {
			case StatusYes:
				eff[n.Key] = StatusYes
			case StatusUnknown:
				eff[n.Key] = StatusUnknown
			default:
				eff[n.Key] = StatusNo
			}
		}
	}
	return eff
}
