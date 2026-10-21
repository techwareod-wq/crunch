package finder

import (
	"math"
	"slices"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

// TestHandle_RegistersWithoutPanic: the middleware registry panics on a
// duplicate path, so a clash fails here instead of at server boot.
func TestHandle_RegistersWithoutPanic(t *testing.T) {
	Handle(&config.AppContext{})
}

func pin(lat, lng float64) *models.GeoPoint {
	return &models.GeoPoint{Type: "Point", Coordinates: [2]float64{lng, lat}}
}

func mustQuery(t *testing.T, req searchRequest) query {
	t.Helper()
	q, msg := parseQuery(req)
	if msg != "" {
		t.Fatalf("parseQuery(%+v): %s", req, msg)
	}
	return q
}

var pune = &point{Lat: 18.52, Lng: 73.86}

func TestParseQuery(t *testing.T) {
	q := mustQuery(t, searchRequest{Place: " Pune ", Pallets: 1000, Type: "Bonded",
		Features: []string{"dg", "pharma", "racked", "dg"}, Limit: 900})
	if q.AreaSqft != 12000 || q.Sort != sortDistance || q.Limit != 500 || !q.Pharma ||
		!slices.Equal(q.Chips, []string{"warehouse.type:bonded", "hazmat", "warehouse.racked"}) {
		t.Errorf("query = %+v", q)
	}
	if q := mustQuery(t, searchRequest{Place: "Pune", AreaSqft: 5000, Pallets: 1000}); q.AreaSqft != 5000 || q.Limit != 100 {
		t.Errorf("areaSqft must win over pallets, limit default 100: %+v", q)
	}
	if q := mustQuery(t, searchRequest{}); q.Place != "" || q.sortMode() != sortCompleteness {
		t.Errorf("empty request: %+v, sortMode %q; want valid, distance → completeness", q, q.sortMode())
	}
	if q := mustQuery(t, searchRequest{Place: "Pune"}); q.sortMode() != sortDistance {
		t.Errorf("with a place, distance stays distance: %q", q.sortMode())
	}
	for name, req := range map[string]searchRequest{
		"bad radius":  {Place: "Pune", RadiusKm: 25},
		"bad type":    {Place: "Pune", Type: "cold"},
		"bad feature": {Place: "Pune", Features: []string{"helipad"}},
		"bad sort":    {Place: "Pune", Sort: "name"},
		"negative":    {Place: "Pune", AreaSqft: -1},
	} {
		if _, msg := parseQuery(req); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAreaFilter(t *testing.T) {
	q := mustQuery(t, searchRequest{Place: "Pune", AreaSqft: 10000})
	// 80% of 10,000 sq ft = 8,000 sq ft ≈ 743.2 sq m.
	for sqm, want := range map[float64]bool{743.3: true, 743.0: false, 2000: true} {
		if _, ok := keep(q, pune, candidate{City: "Pune", TotalSqm: sqm}); ok != want {
			t.Errorf("%v sqm: kept=%t, want %t", sqm, ok, want)
		}
	}
	if _, ok := keep(mustQuery(t, searchRequest{Place: "Pune"}), pune, candidate{City: "Pune", TotalSqm: 1}); !ok {
		t.Error("no area asked: any size passes")
	}
}

func TestTypeExclusion(t *testing.T) {
	q := mustQuery(t, searchRequest{Place: "Pune", Type: "bonded"})
	for name, c := range map[string]struct {
		chips []string
		want  bool
	}{
		"same type":      {[]string{"warehouse.type:bonded"}, true},
		"other type":     {[]string{"warehouse.type:general"}, false},
		"no type (hard)": {[]string{"docks"}, false},
	} {
		if _, ok := keep(q, pune, candidate{City: "Pune", Chips: c.chips}); ok != c.want {
			t.Errorf("%s: kept=%t, want %t", name, ok, c.want)
		}
	}
}

func TestFeaturesAND(t *testing.T) {
	q := mustQuery(t, searchRequest{Place: "Pune", Features: []string{"cold", "racked", "pharma"}})
	all := candidate{City: "Pune", Chips: []string{"cold_storage", "warehouse.racked"}, Fit: []string{"pharma:P"}}
	if _, ok := keep(q, pune, all); !ok {
		t.Error("listing with every feature dropped")
	}
	noRack := candidate{City: "Pune", Chips: []string{"cold_storage"}, Fit: []string{"pharma:F"}}
	notPharma := candidate{City: "Pune", Chips: []string{"cold_storage", "warehouse.racked"}, Fit: []string{"pharma:U"}}
	for name, c := range map[string]candidate{"missing racked": noRack, "pharma only U": notPharma} {
		if _, ok := keep(q, pune, c); ok {
			t.Errorf("%s: kept", name)
		}
	}
	if got := tagsOf(all); !slices.Equal(got, []string{"Cold storage", "Racked", "Pharma fit"}) {
		t.Errorf("tags = %v", got)
	}
}

func TestRadius(t *testing.T) {
	chakan := candidate{City: "Pune", Loc: pin(18.76, 73.86)}       // ≈ 27 km from the centroid
	lonavala := candidate{City: "Lonavala", Loc: pin(18.75, 73.41)} // ≈ 53 km
	mumbai := candidate{City: "Mumbai", Loc: pin(19.08, 72.88)}     // ≈ 122 km
	unpinned := candidate{City: "pune"}                             // same city, no pin
	for _, c := range []struct {
		radius int
		want   []string
	}{
		{0, []string{"Pune", "pune"}},
		{50, []string{"Pune", "pune"}},
		{100, []string{"Pune", "Lonavala", "pune"}},
	} {
		q := mustQuery(t, searchRequest{Place: "PUNE", RadiusKm: c.radius})
		var got []string
		for _, cand := range []candidate{chakan, lonavala, mumbai, unpinned} {
			if _, ok := keep(q, pune, cand); ok {
				got = append(got, cand.City)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("radius %d: kept %v, want %v", c.radius, got, c.want)
		}
	}
	if h, _ := keep(mustQuery(t, searchRequest{Place: "Pune"}), pune, unpinned); h.DistKm != nil {
		t.Errorf("unpinned listing distKm = %v, want nil", *h.DistKm)
	}
	if c := centroidOf([]*models.GeoPoint{pin(18, 73), nil, pin(19, 74)}); c == nil || c.Lat != 18.5 || c.Lng != 73.5 {
		t.Errorf("centroid = %+v, want 18.5,73.5 (nil pins skipped)", c)
	}
}

func TestHaversineAndHubs(t *testing.T) {
	// Mumbai (18.94, 72.84) → Pune (18.52, 73.86): about 118 km.
	if d := haversineKm(18.94, 72.84, 18.52, 73.86); math.Abs(d-118) > 3 {
		t.Errorf("Mumbai–Pune = %.1f km, want ≈118", d)
	}
	if d := haversineKm(12.9, 77.6, 12.9, 77.6); d != 0 {
		t.Errorf("same point = %v, want 0", d)
	}
	h, _ := keep(mustQuery(t, searchRequest{Place: "Mumbai"}), nil, candidate{City: "Mumbai", Loc: pin(19.03, 73.02)})
	if !h.NearPort || !h.NearAirport || h.NearRail {
		t.Errorf("Navi Mumbai near port/airport/rail = %t/%t/%t, want true/true/false", h.NearPort, h.NearAirport, h.NearRail)
	}
	q := mustQuery(t, searchRequest{Place: "Nagpur", NearPort: true})
	if _, ok := keep(q, nil, candidate{City: "Nagpur", Loc: pin(21.15, 79.09)}); ok {
		t.Error("nearPort is hard: Nagpur kept")
	}
}

func TestSortHits(t *testing.T) {
	d := func(v float64) *float64 { return &v }
	price := func(v float64, onRequest bool) *models.Price {
		return &models.Price{PerSqmMonth: v, OnRequest: onRequest}
	}
	hs := []hit{
		{C: candidate{ShortID: "a", TotalSqm: 100, Completeness: 0.5, Price: price(300, false)}, DistKm: d(10)},
		{C: candidate{ShortID: "b", TotalSqm: 900, Completeness: 0.9, Price: price(0, true)}, DistKm: d(10)},
		{C: candidate{ShortID: "c", TotalSqm: 500, Price: price(200, false)}, DistKm: d(2)},
		{C: candidate{ShortID: "d", TotalSqm: 50}},
	}
	for mode, want := range map[string][]string{
		sortDistance: {"c", "b", "a", "d"}, // tie at 10 km → completeness desc; no pin last
		sortArea:     {"b", "c", "a", "d"},
		sortRate:     {"c", "a", "b", "d"}, // on request / unpriced last
	} {
		sortHits(hs, mode)
		var got []string
		for _, h := range hs {
			got = append(got, h.C.ShortID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("sort %s = %v, want %v", mode, got, want)
		}
	}
}

func TestNoPlace(t *testing.T) {
	cs := []candidate{
		{ShortID: "1", Name: "Pune cold", City: "Pune", Loc: pin(18.76, 73.86), TotalSqm: 4000, Completeness: 0.8,
			Chips: []string{"cold_storage", "warehouse.racked"}},
		{ShortID: "2", Name: "Chennai dry", City: "Chennai", Loc: pin(13.10, 80.28), TotalSqm: 900, Completeness: 0.8},
		{ShortID: "3", Name: "Alpha", City: "Guwahati", TotalSqm: 100, Completeness: 0.8}, // no pin
		{ShortID: "4", Name: "Kochi cold", City: "Kochi", Loc: pin(10.05, 76.33), TotalSqm: 2000, Completeness: 0.9,
			Chips: []string{"cold_storage"}},
	}
	run := func(req searchRequest) []string {
		q := mustQuery(t, req)
		var hs []hit
		for _, c := range cs {
			if h, ok := keep(q, nil, c); ok {
				if h.DistKm != nil {
					t.Errorf("%s: distKm = %v without a place, want nil", c.Name, *h.DistKm)
				}
				hs = append(hs, h)
			}
		}
		sortHits(hs, q.sortMode())
		var ids []string
		for _, h := range hs {
			ids = append(ids, h.C.ShortID)
		}
		return ids
	}

	// No filters: every listing, any city; distance → completeness desc, then name.
	if got := run(searchRequest{}); !slices.Equal(got, []string{"4", "3", "2", "1"}) {
		t.Errorf("no place, no filters = %v, want [4 3 2 1]", got)
	}
	// Filters still apply without a place.
	if got := run(searchRequest{Features: []string{"cold"}}); !slices.Equal(got, []string{"4", "1"}) {
		t.Errorf("cold = %v, want [4 1]", got)
	}
	if got := run(searchRequest{Features: []string{"cold", "racked"}}); !slices.Equal(got, []string{"1"}) {
		t.Errorf("cold + racked = %v, want [1]", got)
	}
	if got := run(searchRequest{AreaSqft: 20000}); !slices.Equal(got, []string{"4", "1"}) {
		t.Errorf("area ≥ 16,000 sq ft = %v, want [4 1] (completeness order)", got)
	}
	if got := run(searchRequest{NearPort: true, NearRail: true}); !slices.Equal(got, []string{"2"}) {
		t.Errorf("port + rail = %v, want [2] (Chennai)", got)
	}
	if got := run(searchRequest{Sort: "area"}); !slices.Equal(got, []string{"1", "4", "2", "3"}) {
		t.Errorf("area sort without place = %v, want [1 4 2 3]", got)
	}
	// The prefilter has no place clause.
	if f := mongoFilter(mustQuery(t, searchRequest{}), nil); f["$or"] != nil || len(f) != 1 {
		t.Errorf("no-place prefilter = %v, want status only", f)
	}
}
