package service

// Integration tests against a real MongoDB (spec 04 Tests). Skipped unless
// WH_MONGO_TEST_URI is set, e.g.
//
//	mongod --dbpath /tmp/whdb --port 27999
//	WH_MONGO_TEST_URI=mongodb://127.0.0.1:27999 go test ./internal/services/searchService/... -run Integration
//
// WH_SEARCH_BENCH=1 also runs the 10k-doc latency check.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/services/searchService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var pune = domain.LatLng{Lat: 18.52, Lng: 73.85}

// connectTestDB connects models to a fresh database and drops it after.
func connectTestDB(t *testing.T) {
	t.Helper()
	uri := os.Getenv("WH_MONGO_TEST_URI")
	if uri == "" {
		t.Skip("WH_MONGO_TEST_URI not set")
	}
	name := fmt.Sprintf("wh_search_it_%d", time.Now().UnixNano())
	if err := models.Connect(uri, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.DB.Drop(context.Background()) })
	for _, ensure := range []func(context.Context) error{models.EnsureWarehouseIndexes, models.EnsureWarehouseSearchIndexes} {
		if err := ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

type seed struct {
	n       int
	dLat    float64 // north of Pune, degrees (≈111 km each)
	status  string
	chips   []string
	unk     []string
	nums    []models.NumFact
	fit     []string
	price   float64 // 0 = on request
	sqm     float64
	country string
}

func insertSeeds(t *testing.T, seeds []seed) []primitive.ObjectID {
	t.Helper()
	var ids []primitive.ObjectID
	i := 0
	for _, sd := range seeds {
		for k := 0; k < sd.n; k++ {
			i++
			w := warehouseDoc(i, sd, pune.Lat+sd.dLat, pune.Lng+float64(k)*0.001)
			if err := models.InsertWarehouse(ctx, &w); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, w.ID)
		}
	}
	return ids
}

func warehouseDoc(i int, sd seed, lat, lng float64) models.Warehouse {
	status, country := sd.status, sd.country
	if status == "" {
		status = models.WarehouseLive
	}
	if country == "" {
		country = "IN"
	}
	price := &models.Price{Currency: "INR", PerSqmMonth: sd.price, Basis: domain.BasisPerSqmMonth}
	rent := map[string]any{"amount": int64(sd.price), "currency": "INR", "basis": domain.BasisPerSqmMonth}
	if sd.price == 0 {
		price = &models.Price{Currency: "INR", OnRequest: true}
		rent = map[string]any{"amount": int64(0), "currency": "INR", "onRequest": true}
	}
	sqm := sd.sqm
	if sqm == 0 {
		sqm = 1000
	}
	w := models.Warehouse{
		ShortID: fmt.Sprintf("w%05d", i), Slug: fmt.Sprintf("wh-%d", i), SlugHistory: []string{}, Status: status,
		Name: fmt.Sprintf("Warehouse %d", i), Country: country, City: "Pune", Loc: domain.NewGeoPoint(lat, lng),
		TotalSqm: sqm, Price: price, Completeness: 0.5,
		Live: &models.ListingContent{Attributes: models.Attributes{domain.RootKey: {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{
			"rent":       {V: rent},
			"total_area": {V: map[string]any{"value": sqm, "unit": "sqm", "sqm": sqm}},
		}}}},
		Projection: models.Projection{Chips: nz(sd.chips), Unk: nz(sd.unk), Nums: sd.nums, Fit: nz(sd.fit), NeedsInfo: []string{}},
		CreatedAt:  time.Now(), UpdatedAt: time.Now(),
	}
	if w.Nums == nil {
		w.Nums = []models.NumFact{}
	}
	return w
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func realService() *svc {
	cfg := testConfig()
	return newService(store.NewStore(), staticRules{testSnapshot()}, func() interfaces.Geocoder { return nil },
		func() config.SearchValues { return cfg }, func() string { return "https://cdn" }, time.Now)
}

// fixture: 2 at ~5 km, 3 at ~40 km, 5 at ~80 km, 40 at ~200 km, plus an
// archived listing and a foreign one next door (never returned).
func fixture() []seed {
	cold := []models.NumFact{{K: "cold_storage.temperature", V: -18}}
	return []seed{
		{n: 2, dLat: 0.05, chips: []string{"cold_storage"}, nums: cold, fit: []string{"pharma:F", "food:F"}, price: 300, sqm: 2000},
		{n: 3, dLat: 0.36, chips: []string{"hazmat"}, fit: []string{"pharma:P", "food:U"}, price: 500},
		{n: 5, dLat: 0.72, unk: []string{"cold_storage"}, fit: []string{"pharma:U", "food:F"}, price: 0},
		{n: 40, dLat: 1.8, fit: []string{"pharma:N", "food:N"}, price: 100, sqm: 500},
		{n: 1, dLat: 0.01, status: models.WarehouseArchived, chips: []string{"cold_storage"}, fit: []string{"pharma:F", "food:F"}, price: 300},
		{n: 1, dLat: 0.01, country: "AE", chips: []string{"cold_storage"}, fit: []string{"pharma:F", "food:F"}, price: 300},
	}
}

func near() *domain.SearchLocation {
	p := pune
	return &domain.SearchLocation{Point: &p}
}

func TestIntegrationSearch(t *testing.T) {
	connectTestDB(t)
	ids := insertSeeds(t, fixture())
	s := realService()
	run := func(f domain.SearchFilters) domain.SearchResponse {
		t.Helper()
		r, err := s.Search(ctx, f, searchService.Viewer{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("radius expansion", func(t *testing.T) {
		r := run(domain.SearchFilters{Location: near()})
		if r.Radius.UsedKm != 50 || !r.Radius.Expanded || r.Total != 5 ||
			r.Radius.Message != "Only 2 within 25 km. Showing the nearest within 50 km." {
			t.Errorf("radius = %+v total %d", r.Radius, r.Total)
		}
		if d := *r.Results[0].DistKm; d < 5 || d > 6 || r.Results[0].TotalDisplay != "2,000 sq m" {
			t.Errorf("first card = %+v", r.Results[0])
		}
	})

	t.Run("chip exhausts the rings", func(t *testing.T) {
		r := run(domain.SearchFilters{Location: near(), Chips: []string{"cold_storage"}})
		if r.Radius.UsedKm != 250 || !r.Radius.Exhausted || !r.Radius.CountryLink || r.Total != 2 {
			t.Errorf("radius = %+v total %d", r.Radius, r.Total)
		}
	})

	t.Run("include unverified", func(t *testing.T) {
		r := run(domain.SearchFilters{Location: near(), Chips: []string{"cold_storage"}, IncludeUnverified: true})
		if r.Radius.UsedKm != 100 || r.Total != 7 {
			t.Fatalf("radius = %+v total %d", r.Radius, r.Total)
		}
		unverified := 0
		for _, c := range r.Results {
			if c.Unverified {
				unverified++
			}
		}
		if unverified != 5 {
			t.Errorf("unverified cards = %d", unverified)
		}
	})

	t.Run("multi-industry AND", func(t *testing.T) {
		if r := run(domain.SearchFilters{Industries: []string{"pharma", "food"}}); r.Total != 2 {
			t.Errorf("strict total = %d", r.Total)
		}
		if r := run(domain.SearchFilters{Industries: []string{"pharma", "food"}, IncludeUnverified: true}); r.Total != 10 {
			t.Errorf("unverified total = %d", r.Total)
		}
	})

	t.Run("range filter", func(t *testing.T) {
		r := run(domain.SearchFilters{Ranges: map[string]domain.MinMax{"cold_storage.temperature": {Max: fp(-10)}}})
		if r.Total != 2 {
			t.Errorf("total = %d", r.Total)
		}
	})

	t.Run("price on request after priced", func(t *testing.T) {
		r := run(domain.SearchFilters{Price: &domain.PriceFilter{PerSqmMonthMax: fp(400)}, Limit: 50})
		if r.Total != 47 {
			t.Fatalf("total = %d", r.Total)
		}
		last := r.Results[len(r.Results)-5:]
		for _, c := range last {
			if c.Rate == nil || !c.Rate.OnRequest {
				t.Fatalf("tail card not on request: %+v", c)
			}
		}
		for _, c := range r.Results[:len(r.Results)-5] {
			if c.Rate.OnRequest {
				t.Fatalf("on-request card before priced: %+v", c)
			}
		}
		asc := run(domain.SearchFilters{Sort: domain.SortPriceAsc, Limit: 50})
		if asc.Results[0].Rate.PerSqmMonth != 100 || !asc.Results[len(asc.Results)-1].Rate.OnRequest {
			t.Errorf("price_asc ends = %+v … %+v", asc.Results[0].Rate, asc.Results[len(asc.Results)-1].Rate)
		}
	})

	t.Run("facets", func(t *testing.T) {
		r := run(domain.SearchFilters{})
		if r.Total != 50 || r.Facets.Chips["cold_storage"] != 2 || r.Facets.Chips["hazmat"] != 3 {
			t.Errorf("total %d chips %v", r.Total, r.Facets.Chips)
		}
		if fi := r.Facets.Industries["pharma"]; fi.Fit != 5 || fi.Unverified != 5 {
			t.Errorf("pharma = %+v", fi)
		}
		if p := r.Facets.Price; p == nil || p.Min != 100 || p.Max != 500 {
			t.Errorf("price = %+v", p)
		}
		if a := r.Facets.Area; a == nil || a.Min != 500 || a.Max != 2000 {
			t.Errorf("area = %+v", a)
		}
		if b := r.Facets.Ranges["cold_storage.temperature"]; b.Min != -18 {
			t.Errorf("ranges = %+v", r.Facets.Ranges)
		}
	})

	t.Run("relevance tier", func(t *testing.T) {
		r := run(domain.SearchFilters{Industries: []string{"pharma"}, IncludeUnverified: true, Limit: 50})
		// F (2) before P (3) before U (5).
		got := []string{}
		for _, c := range r.Results {
			got = append(got, c.ShortID)
		}
		if r.Total != 10 || !slices.Equal(got[:2], []string{"w00001", "w00002"}) || !slices.Contains(got[2:5], "w00003") {
			t.Errorf("order = %v", got)
		}
	})

	t.Run("nearest excludes", func(t *testing.T) {
		// Any live listing counts (the AE one next door too); never archived.
		cards, err := s.Nearest(ctx, *domain.NewGeoPoint(pune.Lat, pune.Lng), 3, ids[0])
		if err != nil || len(cards) != 3 || cards[0].ShortID != "w00052" || cards[1].ShortID != "w00002" || cards[1].Rate == nil {
			t.Errorf("nearest = %+v %v", cards, err)
		}
	})

	t.Run("keyword fallback", func(t *testing.T) {
		q := searchService.FallbackQuery{Near: &pune, Exclude: []string{"w00001"}, Limit: 5}
		cards, err := s.Keyword(ctx, "pune", q)
		if err != nil || len(cards) != 5 || slices.ContainsFunc(cards, func(c domain.SearchCard) bool { return c.ShortID == "w00001" }) {
			t.Fatalf("cards = %+v %v", cards, err)
		}
		if cards[0].DistKm == nil {
			t.Error("distance not set")
		}
		// Archived / foreign listings never match; far away nothing is in range.
		delhi := domain.LatLng{Lat: 28.6, Lng: 77.2}
		if cards, _ := s.Keyword(ctx, "pune", searchService.FallbackQuery{Near: &delhi}); len(cards) != 0 {
			t.Errorf("delhi = %d cards", len(cards))
		}
		if cards, _ := s.Keyword(ctx, "nonexistentword", searchService.FallbackQuery{}); len(cards) != 0 {
			t.Errorf("nonsense = %d", len(cards))
		}
	})

	t.Run("map", func(t *testing.T) {
		m, etag, err := s.Map(ctx, nil, "IN", false)
		if err != nil || m.Total != 50 || etag == "" {
			t.Errorf("country map = %d %q %v", m.Total, etag, err)
		}
		m, _, err = s.Map(ctx, &domain.SearchFilters{Chips: []string{"hazmat"}}, "IN", false)
		if err != nil || m.Total != 3 {
			t.Errorf("hazmat map = %d %v", m.Total, err)
		}
		m, _, _ = s.Map(ctx, &domain.SearchFilters{Location: near(), AutoExpand: new(bool)}, "IN", false)
		if m.Total != 2 {
			t.Errorf("25 km map = %d", m.Total)
		}
	})
}

// TestIntegrationLatency10k: p95 of 200 searches over 10k listings.
func TestIntegrationLatency10k(t *testing.T) {
	if os.Getenv("WH_SEARCH_BENCH") == "" {
		t.Skip("WH_SEARCH_BENCH not set")
	}
	connectTestDB(t)
	rng := rand.New(rand.NewSource(1))
	chipSets := [][]string{{"cold_storage"}, {"hazmat"}, {"cold_storage", "hazmat"}, {}}
	fits := []string{"F", "P", "U", "N"}
	docs := make([]any, 0, 10000)
	for i := 0; i < 10000; i++ {
		sd := seed{chips: chipSets[rng.Intn(4)], price: float64(50 + rng.Intn(900)), sqm: float64(200 + rng.Intn(20000)),
			fit:  []string{"pharma:" + fits[rng.Intn(4)], "food:" + fits[rng.Intn(4)]},
			nums: []models.NumFact{{K: "cold_storage.temperature", V: float64(-25 + rng.Intn(30))}}}
		if rng.Intn(10) == 0 {
			sd.unk = []string{"cold_storage"}
		}
		w := warehouseDoc(i+1, sd, 8+rng.Float64()*24, 70+rng.Float64()*18)
		w.ID = primitive.NewObjectID()
		docs = append(docs, w)
	}
	if _, err := models.Collection("warehouses").InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	s := realService()
	var took []time.Duration
	for i := 0; i < 200; i++ {
		p := domain.LatLng{Lat: 8 + rng.Float64()*24, Lng: 70 + rng.Float64()*18}
		f := domain.SearchFilters{Location: &domain.SearchLocation{Point: &p}, Chips: chipSets[i%3],
			Industries: []string{"pharma"}, IncludeUnverified: i%2 == 0, AreaSqm: &domain.MinMax{Min: fp(1000)}}
		start := time.Now()
		if _, err := s.Search(ctx, f, searchService.Viewer{}); err != nil {
			t.Fatal(err)
		}
		took = append(took, time.Since(start))
	}
	check(t, "with location", took)

	// No location: the facets run over every live listing.
	took = took[:0]
	for i := 0; i < 50; i++ {
		start := time.Now()
		if _, err := s.Search(ctx, domain.SearchFilters{Chips: chipSets[i%4], IncludeUnverified: true}, searchService.Viewer{}); err != nil {
			t.Fatal(err)
		}
		took = append(took, time.Since(start))
	}
	check(t, "country-wide", took)
}

func check(t *testing.T, name string, took []time.Duration) {
	t.Helper()
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	p50, p95 := took[len(took)/2], took[len(took)*95/100]
	t.Logf("10k docs, %s: p50 %v, p95 %v", name, p50, p95)
	if p95 > 300*time.Millisecond {
		t.Errorf("%s p95 %v over 300 ms", name, p95)
	}
}
