package service

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/searchService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

// fakeStore records queries and serves canned results.
type fakeStore struct {
	result   models.SearchResult
	rings    []int64
	execs    []store.SearchExec
	ringReqs [][]float64
	points   []models.MapPoint
	mapCalls int
	catalogV int64
	cache    map[string]models.GeocodeCacheEntry
	pins     map[string]models.Pincode
	pinErr   error
	vecErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{cache: map[string]models.GeocodeCacheEntry{}, pins: map[string]models.Pincode{}}
}

func (f *fakeStore) Search(_ context.Context, e store.SearchExec) (models.SearchResult, error) {
	f.execs = append(f.execs, e)
	return f.result, nil
}

func (f *fakeStore) RingCounts(_ context.Context, _ models.GeoPoint, _ models.SearchQuery, ringsM []float64) ([]int64, error) {
	f.ringReqs = append(f.ringReqs, ringsM)
	out := make([]int64, len(ringsM))
	copy(out, f.rings)
	for i := len(f.rings); i < len(out) && i > 0; i++ {
		out[i] = out[i-1]
	}
	return out, nil
}

func (f *fakeStore) MapPoints(context.Context, models.SearchQuery, *models.GeoPoint, float64) ([]models.MapPoint, error) {
	f.mapCalls++
	return f.points, nil
}

func (f *fakeStore) Nearest(context.Context, models.GeoPoint, int, primitive.ObjectID) ([]models.SearchHit, error) {
	return f.result.Hits, nil
}

func (f *fakeStore) CatalogVersion(context.Context) (int64, error) { return f.catalogV, nil }

func (f *fakeStore) Vector(context.Context, string, []float32, int, int, string) ([]models.SearchHit, error) {
	return f.result.Hits, f.vecErr
}

func (f *fakeStore) Text(context.Context, string, string, int) ([]models.SearchHit, error) {
	return f.result.Hits, nil
}

func (f *fakeStore) GetGeocode(_ context.Context, key string, now time.Time) (*models.GeocodeCacheEntry, error) {
	if e, ok := f.cache[key]; ok && e.ExpiresAt.After(now) {
		return &e, nil
	}
	return nil, models.ErrNotFound
}

func (f *fakeStore) PutGeocode(_ context.Context, e models.GeocodeCacheEntry) error {
	f.cache[e.Key] = e
	return nil
}

func (f *fakeStore) Pincode(_ context.Context, code string) (*models.Pincode, error) {
	if f.pinErr != nil {
		return nil, f.pinErr
	}
	if p, ok := f.pins[code]; ok {
		return &p, nil
	}
	return nil, models.ErrNotFound
}

func (f *fakeStore) PincodesByPlace(_ context.Context, name string, _ int) ([]models.Pincode, error) {
	var out []models.Pincode
	for _, p := range f.pins {
		for _, pl := range p.Places {
			if pl == name {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) PincodesByDistrict(_ context.Context, name string, _ int) ([]models.Pincode, error) {
	var out []models.Pincode
	for _, p := range f.pins {
		if p.DistrictLC == name {
			out = append(out, p)
		}
	}
	return out, nil
}

type fakeGeocoder struct {
	res   interfaces.GeocodeResult
	err   error
	calls int
}

func (g *fakeGeocoder) Geocode(context.Context, string, string) (interfaces.GeocodeResult, error) {
	g.calls++
	return g.res, g.err
}

var errDown = errors.New("google down")

type staticRules struct{ s *domain.Snapshot }

func (r staticRules) Snapshot() *domain.Snapshot { return r.s }
func (r staticRules) SnapshotAtLeast(context.Context, int64) (*domain.Snapshot, error) {
	return r.s, nil
}

func testSnapshot() *domain.Snapshot {
	root := domain.RootNode()
	cold := models.AttributeNode{Key: "cold_storage", ParentKey: domain.RootKey, Name: "Cold storage", Public: true, Filterable: true, FilterRow: "Storage", FilterPos: 2,
		Fields: []models.AttributeField{
			{Key: "temperature", Name: "Temperature", Type: domain.TypeNumber, Public: true, Filterable: true, Unit: &models.UnitSpec{Family: domain.DimTemp}},
			{Key: "temp_range", Name: "Range", Type: domain.TypeRange, Public: true, Filterable: true, Unit: &models.UnitSpec{Family: domain.DimTemp}},
			{Key: "temp_type", Name: "Type", Type: domain.TypePick, Public: true, Filterable: true, FilterPos: 1,
				Options: []models.FieldOption{{Key: "chilled", Label: "Chilled", Order: 1}, {Key: "frozen", Label: "Frozen", Order: 2}}},
			{Key: "audited", Name: "Audited", Type: domain.TypeBool, Public: true, Filterable: false},
			{Key: "notes", Name: "Notes", Type: domain.TypeText, Public: true, Filterable: true},
		}}
	secret := models.AttributeNode{Key: "internal", ParentKey: domain.RootKey, Name: "Internal", Public: false, Filterable: true}
	haz := models.AttributeNode{Key: "hazmat", ParentKey: domain.RootKey, Name: "Hazmat", Public: true, Filterable: true}
	pharma := models.Industry{Key: "pharma", Name: "Pharma", Order: 2}
	food := models.Industry{Key: "food", Name: "Food", Order: 1}
	return domain.NewSnapshot(7, []models.AttributeNode{root, cold, secret, haz}, []models.Industry{pharma, food})
}

func testConfig() config.SearchValues {
	return config.SearchValues{DefaultRadiusKm: map[string]int{"IN": 25}, RadiusSteps: []int{25, 50, 100, 250}, MinResults: 3,
		PageLimitDefault: 20, PageLimitMax: 50, Currency: map[string]string{"IN": "INR"}, GeocodeCacheHours: 720,
		BreakerFailures: 2, BreakerOpenSeconds: 60}
}

type harness struct {
	store *fakeStore
	geo   *fakeGeocoder
	svc   *svc
	clock time.Time
}

func newHarness() *harness {
	h := &harness{store: newFakeStore(), geo: &fakeGeocoder{res: interfaces.GeocodeResult{Lat: 18.5, Lng: 73.8, Formatted: "Pune, Maharashtra"}},
		clock: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)}
	h.svc = newService(h.store, staticRules{testSnapshot()}, func() interfaces.Geocoder { return h.geo },
		testConfig, func() string { return "https://cdn" }, func() time.Time { return h.clock })
	return h
}

func errNotFoundGeo() error { return interfaces.ErrGeocodeNotFound }
