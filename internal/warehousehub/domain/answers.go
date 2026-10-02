package domain

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxTextAnswer bounds a text answer (runes).
const MaxTextAnswer = 2000

// CanonicalizeAnswer validates a submitted answer against its definition and
// returns it in stored form: V in its canonical Go type and canonical units,
// Raw kept for number/range. unknown and na carry no value. Used by draft
// saves (03) and needs-info answers.
func CanonicalizeAnswer(d *AttrDef, a Answer) (Answer, error) {
	if d.IsGroup() {
		return a, fmt.Errorf("%s is a group and takes no answer", d.Key)
	}
	if d.Type == TypeCalculated {
		return a, fmt.Errorf("%s is calculated and takes no answer", d.Key)
	}
	switch a.Source {
	case "", SourceSiteVisit, SourceOwner, SourceDocument, SourceOther:
	default:
		return a, fmt.Errorf("%s: unknown source %q", d.Key, a.Source)
	}
	switch a.Status {
	case StatusUnknown, StatusNA:
		a.V, a.Raw = nil, nil
		return a, nil
	case StatusKnown:
	default:
		return a, fmt.Errorf("%s: status must be known, unknown or na", d.Key)
	}

	bad := func(why string) (Answer, error) {
		return a, fmt.Errorf("%s: %s", d.Key, why)
	}
	switch d.Type {
	case TypeBool:
		b, ok := asBool(a.V)
		if !ok {
			return bad("expects true or false")
		}
		a.V, a.Raw = b, nil
	case TypeNumber:
		in, unit := a.V, ""
		if a.Raw != nil {
			in, unit = a.Raw.Value, a.Raw.Unit
		}
		f, ok := asFloat(in)
		if !ok {
			return bad("expects a number")
		}
		c, err := toCanon(d, f, unit)
		if err != nil {
			return bad(err.Error())
		}
		a.V, a.Raw = c, &RawValue{Value: f, Unit: unitOrCanon(d, unit)}
	case TypeRange:
		in, unit := a.V, ""
		if a.Raw != nil {
			in, unit = a.Raw.Value, a.Raw.Unit
		}
		r, ok := asRange(in)
		if !ok || r.Min > r.Max {
			return bad("expects {min, max} with min ≤ max")
		}
		lo, err := toCanon(d, r.Min, unit)
		if err != nil {
			return bad(err.Error())
		}
		hi, _ := toCanon(d, r.Max, unit)
		a.V, a.Raw = Range{Min: lo, Max: hi}, &RawValue{Value: r, Unit: unitOrCanon(d, unit)}
	case TypePick:
		s, ok := asString(a.V)
		if !ok || !d.HasAllowedValue(s) {
			return bad("expects one of the allowed values")
		}
		a.V, a.Raw = s, nil
	case TypeMulti:
		ss, ok := asStrings(a.V)
		if !ok {
			return bad("expects a list of allowed values")
		}
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			if !d.HasAllowedValue(s) {
				return bad(fmt.Sprintf("%q is not an allowed value", s))
			}
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		a.V, a.Raw = out, nil
	case TypeText:
		s, ok := asString(a.V)
		s = strings.TrimSpace(s)
		if !ok || s == "" || utf8.RuneCountInString(s) > MaxTextAnswer {
			return bad(fmt.Sprintf("expects text of 1–%d characters", MaxTextAnswer))
		}
		a.V, a.Raw = s, nil
	case TypeMoney:
		m, ok := asMoney(a.V)
		if !ok || m.Amount < 0 || len(m.Currency) != 3 {
			return bad("expects {amount (minor units, ≥ 0), currency (ISO 4217)}")
		}
		m.Currency = strings.ToUpper(m.Currency)
		a.V, a.Raw = m, nil
	default:
		return bad("unsupported type")
	}
	return a, nil
}

func toCanon(d *AttrDef, v float64, unit string) (float64, error) {
	if d.Unit == nil {
		if unit != "" {
			return 0, fmt.Errorf("takes no unit")
		}
		return v, nil
	}
	return ToCanonical(d.Unit.Dimension, v, unit)
}

func unitOrCanon(d *AttrDef, unit string) string {
	if unit != "" || d.Unit == nil {
		return unit
	}
	return CanonicalUnit(d.Unit.Dimension)
}
