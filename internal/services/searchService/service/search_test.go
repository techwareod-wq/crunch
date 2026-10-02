package service

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func fp(v float64) *float64 { return &v }

func TestNormalize(t *testing.T) {
	snap, cfg := testSnapshot(), testConfig()
	in := domain.SearchFilters{
		Location:   &domain.SearchLocation{Place: "  navi   mumbai ", Country: "in"},
		RadiusKm:   900,
		Industries: []string{"pharma", "ghost", "pharma"},
		Chips:      []string{"cold_storage", "cold_storage.temp_type:frozen", "cold_storage.temp_type:boiled", "internal", "hazmat:x", "cold_storage.audited", "warehouse"},
		Ranges: map[string]domain.MinMax{
			"cold_storage.temperature": {Max: fp(-10)},
			"cold_storage.temp_range":  {Min: fp(-20), Max: fp(-5)},
			"cold_storage.notes":       {Min: fp(1)},
			"cold_storage.ghost":       {Min: fp(1)},
			"cold_storage.empty":       {},
		},
		Price: &domain.PriceFilter{PerSqmMonthMax: fp(500)},
		Limit: 500,
	}
	n, q, dropped, err := normalize(snap, in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n.Location.Place != "navi mumbai" || n.Location.Country != "IN" || q.Country != "IN" {
		t.Errorf("location = %+v", n.Location)
	}
	if n.RadiusKm != 250 || !*n.AutoExpand || n.Limit != 50 || n.Page != 1 || n.Sort != domain.SortDistance {
		t.Errorf("defaults: radius %d expand %v limit %d page %d sort %s", n.RadiusKm, *n.AutoExpand, n.Limit, n.Page, n.Sort)
	}
	if !slices.Equal(n.Industries, []string{"pharma"}) {
		t.Errorf("industries = %v", n.Industries)
	}
	if !slices.Equal(n.Chips, []string{"cold_storage", "cold_storage.temp_type:frozen"}) {
		t.Errorf("chips = %v", n.Chips)
	}
	if !slices.Equal(q.Chips[1].Unk, []string{"cold_storage.temp_type", "cold_storage"}) {
		t.Errorf("chip unk = %v", q.Chips[1].Unk)
	}
	want := []string{"industry:ghost", "chip:cold_storage.temp_type:boiled", "chip:internal", "chip:hazmat:x", "chip:cold_storage.audited", "chip:warehouse",
		"range:cold_storage.ghost", "range:cold_storage.notes"}
	for _, w := range want {
		if !slices.Contains(dropped, w) {
			t.Errorf("not dropped: %s (dropped %v)", w, dropped)
		}
	}
	if len(q.Ranges) != 2 {
		t.Fatalf("ranges = %+v", q.Ranges)
	}
	// Sorted keys: temp_range first. A range field maps min → _max ≥, max → _min ≤.
	rr := q.Ranges[0].Conds
	if rr[0].K != "cold_storage.temp_range_max" || *rr[0].Gte != -20 || rr[1].K != "cold_storage.temp_range_min" || *rr[1].Lte != -5 {
		t.Errorf("range conds = %+v", rr)
	}
	if q.PriceCurrency != "INR" || *q.PriceMax != 500 || n.Price.Currency != "INR" {
		t.Errorf("price = %+v", n.Price)
	}

	for name, bad := range map[string]domain.SearchFilters{
		"sort":        {Sort: "random"},
		"min>max":     {AreaSqm: &domain.MinMax{Min: fp(10), Max: fp(5)}},
		"negative":    {Price: &domain.PriceFilter{PerSqmMonthMin: fp(-1)}},
		"neg area":    {AreaSqm: &domain.MinMax{Max: fp(-1)}},
		"bad point":   {Location: &domain.SearchLocation{Point: &domain.LatLng{Lat: 91}}},
		"range order": {Ranges: map[string]domain.MinMax{"cold_storage.temperature": {Min: fp(5), Max: fp(1)}}},
	} {
		var ve *searchService.ValidationError
		if _, _, _, err := normalize(snap, bad, cfg); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// No location: relevance; empty location object is dropped.
	n, _, _, _ = normalize(snap, domain.SearchFilters{Location: &domain.SearchLocation{Country: "IN"}}, cfg)
	if n.Location != nil || n.Sort != domain.SortRelevance || n.RadiusKm != 25 {
		t.Errorf("no-location defaults = %+v", n)
	}
}

func TestPickRing(t *testing.T) {
	rings := []int{25, 50, 100, 250}
	cases := []struct {
		counts            []int64
		auto              bool
		used              int
		expanded, exhaust bool
		msg               string
	}{
		{[]int64{5, 5, 5, 5}, true, 25, false, false, ""},
		{[]int64{0, 1, 4, 9}, true, 100, true, false, "No warehouses within 25 km. Showing the nearest within 100 km."},
		{[]int64{1, 1, 3, 9}, true, 100, true, false, "Only 1 within 25 km. Showing the nearest within 100 km."},
		{[]int64{0, 0, 1, 2}, true, 250, true, true, "No warehouses within 25 km. Showing the nearest within 250 km."},
		{[]int64{0, 0, 0, 9}, false, 25, false, false, ""},
	}
	for _, c := range cases {
		r := pickRing(rings, c.counts, 3, c.auto)
		if r.UsedKm != c.used || r.Expanded != c.expanded || r.Exhausted != c.exhaust || r.CountryLink != c.exhaust || r.Message != c.msg {
			t.Errorf("%v auto=%v → %+v", c.counts, c.auto, r)
		}
	}
}

func TestSearchFlow(t *testing.T) {
	h := newHarness()
	h.store.rings = []int64{0, 4}
	dist := 30400.0
	h.store.result = models.SearchResult{Total: 1, Hits: []models.SearchHit{{
		ShortID: "abc", Slug: "s", Name: "Hub", DistM: &dist, TotalSqm: 929.03, CoverKey: "wh/1.jpg",
		Price: &models.Price{Currency: "INR", PerSqmMonth: 32291.73},
		Rent:  &models.FieldValue{V: map[string]any{"amount": 3000, "currency": "INR", "basis": "per_sqft_month"}},
		Area:  &models.FieldValue{V: map[string]any{"value": 10000.0, "unit": "sqft", "sqm": 929.03}},
		Fit:   []string{"pharma:U", "food:P"}, Chips: []string{"cold_storage"},
	}}, ChipCounts: map[string]int64{"cold_storage": 1}, FitCounts: map[string]int64{"pharma:U": 1, "food:P": 1, "food:N": 3}}

	resp, err := h.svc.Search(ctx, domain.SearchFilters{
		Location: &domain.SearchLocation{PostalCode: "411 001"}, Industries: []string{"pharma"},
		Chips: []string{"cold_storage"}, IncludeUnverified: true,
	}, searchService.Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied.GeocodeSource != domain.GeoSourceGoogle || resp.Applied.ResolvedPoint.Lat != 18.5 {
		t.Errorf("applied = %+v", resp.Applied)
	}
	if r := resp.Radius; r.RequestedKm != 25 || r.UsedKm != 50 || !r.Expanded {
		t.Errorf("radius = %+v", r)
	}
	if got := h.store.ringReqs[0]; !slices.Equal(got, []float64{25000, 50000, 100000, 250000}) {
		t.Errorf("rings = %v", got)
	}
	e := h.store.execs[0]
	if e.MaxMeters != 50000 || e.Near == nil || e.Sort.Mode != domain.SortDistance || !e.Sort.Near {
		t.Errorf("exec = %+v", e)
	}
	c := resp.Results[0]
	if *c.DistKm != 30.4 || c.TotalDisplay != "10,000 sq ft" || c.CoverURL != "https://cdn/wh/1.jpg" {
		t.Errorf("card = %+v", c)
	}
	if c.Rate == nil || c.Rate.Amount != 3000 || c.Rate.PerSqmMonth != 32291.73 {
		t.Errorf("rate = %+v", c.Rate)
	}
	if !slices.Equal(c.Industries, []string{"food"}) || !c.Unverified {
		t.Errorf("industries %v unverified %v", c.Industries, c.Unverified)
	}
	if fi := resp.Facets.Industries; fi["pharma"].Unverified != 1 || fi["food"].Fit != 1 || len(fi) != 2 {
		t.Errorf("industry facets = %+v", fi)
	}
	// Second search with the same postal code hits the cache.
	if _, err := h.svc.Search(ctx, domain.SearchFilters{Location: &domain.SearchLocation{PostalCode: "411001"}}, searchService.Viewer{}); err != nil {
		t.Fatal(err)
	}
	if h.geo.calls != 1 {
		t.Errorf("geocoder calls = %d", h.geo.calls)
	}
}

func TestSearchDegradesWithoutLocation(t *testing.T) {
	h := newHarness()
	h.geo.err = errDown
	resp, err := h.svc.Search(ctx, domain.SearchFilters{Location: &domain.SearchLocation{Place: "Atlantis"}}, searchService.Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.Degraded, []string{domain.DegradedGeocode}) || resp.Radius != nil {
		t.Errorf("degraded = %v radius = %+v", resp.Degraded, resp.Radius)
	}
	if e := h.store.execs[0]; e.Near != nil || e.Sort.Mode != domain.SortRelevance || resp.Applied.Filters.Location != nil {
		t.Errorf("exec = %+v", e)
	}
}

func TestGeoFallbacksAndBreaker(t *testing.T) {
	h := newHarness()
	h.store.pins["411001"] = models.Pincode{Code: "411001", Lat: 18.52, Lng: 73.85, Place: "Pune City", District: "Pune", DistrictLC: "pune", Places: []string{"pune city", "camp"}}
	h.store.pins["411002"] = models.Pincode{Code: "411002", Lat: 18.50, Lng: 73.87, Place: "Shivajinagar", District: "Pune", DistrictLC: "pune", Places: []string{"shivajinagar"}}
	h.geo.err = errDown
	in := domain.SearchLocation{PostalCode: "411001", Country: "IN"}

	r, err := h.svc.geo.resolve(ctx, in)
	if err != nil || r.Source != domain.GeoSourcePincode || r.Label != "411001, Pune City, Pune" {
		t.Fatalf("pincode fallback = %+v %v", r, err)
	}
	if len(h.store.cache) != 0 {
		t.Error("a pincode answer was cached")
	}
	// Second failure opens the breaker (threshold 2): Google isn't called.
	h.svc.geo.resolve(ctx, in)
	h.svc.geo.resolve(ctx, in)
	if h.geo.calls != 2 {
		t.Errorf("calls with breaker open = %d", h.geo.calls)
	}
	// After the window, one call goes through; success closes it.
	h.clock = h.clock.Add(61 * time.Second)
	h.geo.err = nil
	if r, _ := h.svc.geo.resolve(ctx, in); r.Source != domain.GeoSourceGoogle || h.geo.calls != 3 {
		t.Errorf("half-open = %+v calls %d", r, h.geo.calls)
	}

	// Place: a district name → centroid of its pincodes; unknown → not found.
	h.geo.err = errDown
	h.clock = h.clock.Add(time.Hour)
	r, err = h.svc.geo.resolve(ctx, domain.SearchLocation{Place: "Pune", Country: "IN"})
	if err != nil || math.Abs(r.Point.Lat-18.51) > 1e-9 || r.Label != "Pune" {
		t.Errorf("district = %+v %v", r, err)
	}
	h.geo.err = nil
	h.geo.res.Lat = 0
	h.geo.err = errNotFoundGeo()
	if _, err := h.svc.geo.resolve(ctx, domain.SearchLocation{Place: "Atlantis", Country: "IN"}); !errors.Is(err, searchService.ErrNotFound) {
		t.Errorf("unknown place = %v", err)
	}
	// Outage + no pincode hit = unavailable.
	h.geo.err = errDown
	if _, err := h.svc.geo.resolve(ctx, domain.SearchLocation{Place: "Atlantis", Country: "IN"}); !errors.Is(err, searchService.ErrGeoUnavailable) {
		t.Errorf("outage = %v", err)
	}
}

func TestResolve(t *testing.T) {
	h := newHarness()
	r, err := h.svc.Resolve(ctx, " 411 001 ", "")
	if err != nil || r.Source != domain.GeoSourceGoogle {
		t.Fatalf("%+v %v", r, err)
	}
	if _, ok := h.store.cache["IN|pin|411001"]; !ok {
		t.Errorf("cache keys = %v", h.store.cache)
	}
	var ve *searchService.ValidationError
	if _, err := h.svc.Resolve(ctx, "  ", "IN"); !errors.As(err, &ve) {
		t.Errorf("empty q = %v", err)
	}
}

func TestMapBandsAndCache(t *testing.T) {
	h := newHarness()
	pt := func(id string, lat, lng, price float64) models.MapPoint {
		p := models.MapPoint{ShortID: id, Loc: *domain.NewGeoPoint(lat, lng)}
		if price > 0 {
			p.Price = &models.Price{PerSqmMonth: price}
		}
		return p
	}
	h.store.points = []models.MapPoint{pt("a", 18, 73, 100), pt("b", 19, 72, 200), pt("c", 20, 74, 300), pt("d", 17, 75, 0)}
	h.store.catalogV = 4
	resp, etag, err := h.svc.Map(ctx, nil, "in")
	if err != nil || etag != `"map-IN-4"` || resp.Total != 4 {
		t.Fatalf("%+v %q %v", resp, etag, err)
	}
	bands := []any{resp.Points[0][3], resp.Points[1][3], resp.Points[2][3], resp.Points[3][3]}
	if !slices.Equal(bands, []any{1, 2, 3, 0}) {
		t.Errorf("bands = %v", bands)
	}
	if !slices.Equal(resp.BBox, []float64{72, 17, 75, 20}) {
		t.Errorf("bbox = %v", resp.BBox)
	}
	h.svc.Map(ctx, nil, "IN")
	if h.store.mapCalls != 1 {
		t.Errorf("country view not cached: %d calls", h.store.mapCalls)
	}
	h.store.catalogV = 5
	if _, etag, _ := h.svc.Map(ctx, nil, "IN"); etag != `"map-IN-5"` || h.store.mapCalls != 2 {
		t.Errorf("cache not invalidated: %q %d", etag, h.store.mapCalls)
	}
	// Filtered maps are never cached and carry no etag.
	if _, etag, _ := h.svc.Map(ctx, &domain.SearchFilters{}, "IN"); etag != "" || h.store.mapCalls != 3 {
		t.Errorf("filtered = %q %d", etag, h.store.mapCalls)
	}
}

func TestCatalog(t *testing.T) {
	h := newHarness()
	c, etag := h.svc.Catalog("")
	if etag != `"catalog-IN-7"` || c.Currency != "INR" || c.DefaultRadiusKm != 25 {
		t.Errorf("header = %+v %q", c, etag)
	}
	if len(c.ChipRows) != 2 || c.ChipRows[0].Row != "Storage" || c.ChipRows[1].Row != "Features" {
		t.Fatalf("rows = %+v", c.ChipRows)
	}
	var keys []string
	for _, ch := range c.ChipRows[0].Chips {
		keys = append(keys, ch.Key)
	}
	// Pick options (pos 1) before the node chip (pos 2); internal (private) absent.
	if !slices.Equal(keys, []string{"cold_storage.temp_type:chilled", "cold_storage.temp_type:frozen", "cold_storage"}) {
		t.Errorf("storage chips = %v", keys)
	}
	types := map[string]string{}
	for _, r := range c.Ranges {
		if r.Unit != "C" {
			t.Errorf("unit = %+v", r)
		}
		types[r.Key] = r.Type
	}
	if len(types) != 2 || types["cold_storage.temperature"] != "number" || types["cold_storage.temp_range"] != "range" {
		t.Errorf("ranges = %+v", c.Ranges)
	}
	if len(c.Industries) != 2 || c.Industries[0].Key != "food" {
		t.Errorf("industries = %+v", c.Industries)
	}
}

type recLogger struct {
	mu sync.Mutex
	ev []domain.SearchEvent
	wg *sync.WaitGroup
}

func (l *recLogger) Log(_ context.Context, e domain.SearchEvent) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
	l.wg.Done()
}

func TestSearchLogs(t *testing.T) {
	h := newHarness()
	var wg sync.WaitGroup
	l := &recLogger{wg: &wg}
	h.svc.SetLogger(l)
	wg.Add(1)
	resp, _ := h.svc.Search(ctx, domain.SearchFilters{Text: "cold"}, searchService.Viewer{UserID: "u1", Staff: true})
	wg.Wait()
	if e := l.ev[0]; e.SearchID != resp.SearchID || !e.Staff || e.Source != "structured" || e.Filters.Text != "cold" {
		t.Errorf("event = %+v", e)
	}
}

func TestGroupThousands(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", 1234567.5: "1,234,567.5", 10000: "10,000"} {
		if got := groupThousands(in); got != want {
			t.Errorf("%v → %q", in, got)
		}
	}
}
