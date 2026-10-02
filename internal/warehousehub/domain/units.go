package domain

import (
	"fmt"
	"strings"
)

// Dimension is the physical quantity a number/range attribute measures. Every
// dimension has one canonical unit; answers store `v` in it and keep what the
// editor typed in `raw` (spec 00 Units).
type Dimension string

const (
	DimArea   Dimension = "area"   // canonical sqm
	DimTemp   Dimension = "temp"   // canonical C
	DimLoad   Dimension = "load"   // canonical t/m2
	DimMass   Dimension = "mass"   // canonical MT
	DimLength Dimension = "length" // canonical m
	DimCount  Dimension = "count"  // unitless
)

// Unit symbols accepted on input.
const (
	UnitSqm     = "sqm"
	UnitSqft    = "sqft"
	UnitC       = "C"
	UnitF       = "F"
	UnitTPerSqm = "t/m2"
	UnitMT      = "MT"
	UnitM       = "m"
	UnitFt      = "ft"
)

// SqmPerSqft is the exact area conversion factor.
const SqmPerSqft = 0.09290304

// MPerFt is the exact length conversion factor.
const MPerFt = 0.3048

// unitTable maps each dimension to its canonical unit and the input units it
// accepts, each with a converter to canonical.
var unitTable = map[Dimension]struct {
	canonical string
	toCanon   map[string]func(float64) float64
}{
	DimArea: {UnitSqm, map[string]func(float64) float64{
		UnitSqm:  func(v float64) float64 { return v },
		UnitSqft: func(v float64) float64 { return v * SqmPerSqft },
	}},
	DimTemp: {UnitC, map[string]func(float64) float64{
		UnitC: func(v float64) float64 { return v },
		UnitF: func(v float64) float64 { return (v - 32) * 5 / 9 },
	}},
	DimLoad: {UnitTPerSqm, map[string]func(float64) float64{
		UnitTPerSqm: func(v float64) float64 { return v },
	}},
	DimMass: {UnitMT, map[string]func(float64) float64{
		UnitMT: func(v float64) float64 { return v },
	}},
	DimLength: {UnitM, map[string]func(float64) float64{
		UnitM:  func(v float64) float64 { return v },
		UnitFt: func(v float64) float64 { return v * MPerFt },
	}},
	DimCount: {"", map[string]func(float64) float64{
		"": func(v float64) float64 { return v },
	}},
}

// KnownDimension reports whether d is a Dimension constant.
func KnownDimension(d Dimension) bool {
	_, ok := unitTable[d]
	return ok
}

// CanonicalUnit returns d's canonical unit symbol ("" for count).
func CanonicalUnit(d Dimension) string {
	return unitTable[d].canonical
}

// AcceptsUnit reports whether unit is a valid input unit for d.
func AcceptsUnit(d Dimension, unit string) bool {
	t, ok := unitTable[d]
	if !ok {
		return false
	}
	_, ok = t.toCanon[unit]
	return ok
}

// ToCanonical converts v in unit to d's canonical unit. An empty unit means
// "already canonical".
func ToCanonical(d Dimension, v float64, unit string) (float64, error) {
	t, ok := unitTable[d]
	if !ok {
		return 0, fmt.Errorf("unknown dimension %q", d)
	}
	if unit == "" {
		unit = t.canonical
	}
	conv, ok := t.toCanon[strings.TrimSpace(unit)]
	if !ok {
		return 0, fmt.Errorf("unit %q not accepted for %s", unit, d)
	}
	return conv(v), nil
}

// FromCanonical converts a canonical value of d into unit (the inverse of
// ToCanonical). Every conversion in the table is linear, so the inverse is
// read off two points.
func FromCanonical(d Dimension, v float64, unit string) (float64, error) {
	t, ok := unitTable[d]
	if !ok {
		return 0, fmt.Errorf("unknown dimension %q", d)
	}
	if unit == "" {
		unit = t.canonical
	}
	conv, ok := t.toCanon[strings.TrimSpace(unit)]
	if !ok {
		return 0, fmt.Errorf("unit %q not accepted for %s", unit, d)
	}
	offset, slope := conv(0), conv(1)-conv(0)
	return (v - offset) / slope, nil
}
