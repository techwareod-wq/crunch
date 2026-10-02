package domain

import (
	"math"
	"testing"
)

func TestNormalizePrice(t *testing.T) {
	cases := []struct {
		name   string
		m      Money
		sqm    float64
		want   float64
		approx bool
		ok     bool
	}{
		{"per sq m", Money{Amount: 500, Currency: "INR", Basis: BasisPerSqmMonth}, 0, 500, false, true},
		{"per sq ft ×10.7639", Money{Amount: 100, Currency: "INR", Basis: BasisPerSqftMonth}, 0, 1076.39, false, true},
		{"flat ÷ total area (D-131)", Money{Amount: 1000000, Currency: "INR", Basis: BasisFlatMonth}, 2000, 500, true, true},
		{"flat without area", Money{Amount: 1000000, Currency: "INR", Basis: BasisFlatMonth}, 0, 0, false, false},
		{"unknown basis", Money{Amount: 1, Currency: "INR", Basis: "weekly"}, 100, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := NormalizePrice(c.m, c.sqm)
			if ok != c.ok || (ok && (math.Abs(p.PerSqmMonth-c.want) > 0.01 || p.Approx != c.approx || p.Currency != "INR")) {
				t.Fatalf("got %+v %v", p, ok)
			}
		})
	}
	p, ok := NormalizePrice(Money{Currency: "INR", OnRequest: true}, 0)
	if !ok || !p.OnRequest {
		t.Fatalf("on request: %+v", p)
	}
}

func TestCompleteness(t *testing.T) {
	s := fixtureSnap()
	a := baseAttrs()
	now := Evaluate(s, a, zeroTime)
	c, v := Completeness(s, a, now)
	if c <= 0 || c >= 1 || v != 0 {
		t.Fatalf("completeness = %v verified = %v", c, v)
	}
}
