package finder

import (
	"math"
	"slices"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

// Tests for the /match wizard scoring. The route-registration smoke test
// lives in search_test.go (it covers both endpoints).

func TestAreaPoints(t *testing.T) {
	for _, c := range []struct {
		ratio float64
		want  int
	}{
		{0.79, 0}, {0.8, 12}, {0.99, 12}, {1.0, 20}, {1.25, 20}, {1.5, 20},
		{1.51, 10}, {3.0, 10}, {3.01, 4}, {10, 4},
	} {
		if got := areaPoints(c.ratio); got != c.want {
			t.Errorf("areaPoints(%v) = %d, want %d", c.ratio, got, c.want)
		}
	}
}

func TestTypeRule(t *testing.T) {
	general := []string{"docks", "warehouse.type:general"}
	bonded := []string{"warehouse.type:bonded"}
	unknown := []string{"docks"}
	for _, c := range []struct {
		name  string
		want  string
		chips []string
		out   bool
	}{
		{"any type keeps all", "", general, false},
		{"same type kept", "bonded", bonded, false},
		{"different type excluded", "bonded", general, true},
		{"unknown type kept", "bonded", unknown, false},
	} {
		if got := typeExcluded(c.want, c.chips); got != c.out {
			t.Errorf("%s: typeExcluded = %t, want %t", c.name, got, c.out)
		}
	}

	q := criteria{City: "Pune", Type: "bonded", NeededSqm: 1000}
	if s := score(q, matchCandidate{TotalSqm: 1000, Chips: bonded}); s.Score != 50 || s.Max != 50 || s.TypeUnknown {
		t.Errorf("bonded listing: score %d/%d unknown=%t, want 50/50 known", s.Score, s.Max, s.TypeUnknown)
	}
	if s := score(q, matchCandidate{TotalSqm: 1000, Chips: unknown}); s.Score != 20 || s.Max != 50 || !s.TypeUnknown {
		t.Errorf("unknown-type listing: score %d/%d unknown=%t, want 20/50 unknown", s.Score, s.Max, s.TypeUnknown)
	}
}

func TestIndustryPoints(t *testing.T) {
	fit := []string{"pharma:F", "ecommerce:P", "chemicals:U"}
	for ind, want := range map[string]int{"pharma": 25, "ecommerce": 12, "chemicals": 4, "food": 0} {
		if got, _ := industryPoints(fit, ind); got != want {
			t.Errorf("industryPoints(%s) = %d, want %d", ind, got, want)
		}
	}
	if got, _ := industryPoints([]string{"pharma:N"}, "pharma"); got != 0 {
		t.Errorf("N verdict = %d, want 0", got)
	}

	// Max counts only the selected industries; matchPercent rounds.
	q := criteria{City: "Pune", NeededSqm: 1000, Industries: []string{"pharma", "ecommerce"}}
	s := score(q, matchCandidate{TotalSqm: 1000, Fit: fit})
	if s.Score != 57 || s.Max != 70 || s.Percent() != 81 {
		t.Errorf("score %d/%d = %d%%, want 57/70 = 81%%", s.Score, s.Max, s.Percent())
	}
	if !slices.ContainsFunc(s.Matched, func(m match) bool { return m.Label == "Pharma fit" }) {
		t.Errorf("matched %v lacks Pharma fit", s.Matched)
	}
}

func TestMatchHaversineAndHubs(t *testing.T) {
	// Mumbai (18.94, 72.84) → Pune (18.52, 73.86): about 118 km.
	if d := haversineKm(18.94, 72.84, 18.52, 73.86); math.Abs(d-118) > 3 {
		t.Errorf("Mumbai–Pune = %.1f km, want ≈118", d)
	}
	if d := haversineKm(12.9, 77.6, 12.9, 77.6); d != 0 {
		t.Errorf("same point = %v, want 0", d)
	}
	// loc is GeoJSON [lng, lat].
	navi := &models.GeoPoint{Type: "Point", Coordinates: [2]float64{73.02, 19.03}}
	if !nearLoc(navi, ports, nearPortKm) {
		t.Error("Navi Mumbai should be near JNPT")
	}
	nagpur := &models.GeoPoint{Type: "Point", Coordinates: [2]float64{79.09, 21.15}}
	if nearLoc(nagpur, ports, nearPortKm) || nearLoc(nagpur, rail, nearRailKm) || nearLoc(nil, ports, nearPortKm) {
		t.Error("Nagpur (and a missing pin) should be near no hub")
	}
}

func TestRankAndReason(t *testing.T) {
	q := criteria{City: "Pune", Type: "bonded", NeededSqm: 1000, NearPort: true}
	jnpt := &models.GeoPoint{Type: "Point", Coordinates: [2]float64{72.95, 18.95}}
	got := rank(q, []matchCandidate{
		{ShortID: "gen", TotalSqm: 1000, Chips: []string{"warehouse.type:general"}, Loc: jnpt},
		{ShortID: "unk-lo", TotalSqm: 1000, Completeness: 0.2},
		{ShortID: "unk-hi", TotalSqm: 1000, Completeness: 0.9},
		{ShortID: "bond", TotalSqm: 1000, Chips: []string{"warehouse.type:bonded"}, Loc: jnpt},
	}, 3)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.C.ShortID)
	}
	if !slices.Equal(ids, []string{"bond", "unk-hi", "unk-lo"}) {
		t.Fatalf("rank = %v, want [bond unk-hi unk-lo] (general excluded, completeness tie-break)", ids)
	}
	if r := reason(got[0].Matched); r != "Matches your city, bonded type, your size and is near a port" {
		t.Errorf("reason = %q", r)
	}
}
