package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the search service's data access (Mongo in production).
type Store interface {
	Search(ctx context.Context, e SearchExec) (models.SearchResult, error)
	// RingCounts counts matches within each ring (metres), cumulatively.
	RingCounts(ctx context.Context, near models.GeoPoint, q models.SearchQuery, ringsM []float64) ([]int64, error)
	MapPoints(ctx context.Context, q models.SearchQuery, near *models.GeoPoint, maxMeters float64) ([]models.MapPoint, error)
	Nearest(ctx context.Context, near models.GeoPoint, limit int, exclude primitive.ObjectID) ([]models.SearchHit, error)
	CatalogVersion(ctx context.Context) (int64, error)
	Vector(ctx context.Context, index string, vec []float32, numCandidates, limit int, country string) ([]models.SearchHit, error)
	Text(ctx context.Context, text, country string, limit int) ([]models.SearchHit, error)

	GetGeocode(ctx context.Context, key string, now time.Time) (*models.GeocodeCacheEntry, error)
	PutGeocode(ctx context.Context, e models.GeocodeCacheEntry) error
	Pincode(ctx context.Context, code string) (*models.Pincode, error)
	PincodesByPlace(ctx context.Context, name string, limit int) ([]models.Pincode, error)
	PincodesByDistrict(ctx context.Context, name string, limit int) ([]models.Pincode, error)
}

// SearchExec is one results query (models.SearchExec with the query still
// un-built, so fakes can evaluate it).
type SearchExec struct {
	Query     models.SearchQuery
	Near      *models.GeoPoint
	MaxMeters float64
	Sort      models.SearchSort
	Skip      int
	Limit     int
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) Search(ctx context.Context, e SearchExec) (models.SearchResult, error) {
	return models.RunSearch(ctx, models.SearchExec{Match: models.SearchMatch(e.Query), Near: e.Near, MaxMeters: e.MaxMeters,
		Sort: e.Sort, Skip: e.Skip, Limit: e.Limit})
}

func (s *store) RingCounts(ctx context.Context, near models.GeoPoint, q models.SearchQuery, ringsM []float64) ([]int64, error) {
	return models.SearchRingCounts(ctx, near, models.SearchMatch(q), ringsM)
}

func (s *store) MapPoints(ctx context.Context, q models.SearchQuery, near *models.GeoPoint, maxMeters float64) ([]models.MapPoint, error) {
	return models.SearchMapPoints(ctx, models.SearchMatch(q), near, maxMeters)
}

func (s *store) Nearest(ctx context.Context, near models.GeoPoint, limit int, exclude primitive.ObjectID) ([]models.SearchHit, error) {
	return models.NearestLiveWarehouses(ctx, near, limit, exclude)
}

func (s *store) CatalogVersion(ctx context.Context) (int64, error) {
	return models.GetCounter(ctx, models.CounterCatalogVersion)
}

func (s *store) GetGeocode(ctx context.Context, key string, now time.Time) (*models.GeocodeCacheEntry, error) {
	return models.FindGeocodeCache(ctx, key, now)
}

func (s *store) PutGeocode(ctx context.Context, e models.GeocodeCacheEntry) error {
	return models.PutGeocodeCache(ctx, e)
}

func (s *store) Pincode(ctx context.Context, code string) (*models.Pincode, error) {
	return models.FindPincode(ctx, code)
}

func (s *store) PincodesByPlace(ctx context.Context, name string, limit int) ([]models.Pincode, error) {
	return models.FindPincodesByPlace(ctx, name, limit)
}

func (s *store) PincodesByDistrict(ctx context.Context, name string, limit int) ([]models.Pincode, error) {
	return models.FindPincodesByDistrict(ctx, name, limit)
}

func (s *store) Vector(ctx context.Context, index string, vec []float32, numCandidates, limit int, country string) ([]models.SearchHit, error) {
	return models.VectorSearchWarehouses(ctx, index, vec, numCandidates, limit, country)
}

func (s *store) Text(ctx context.Context, text, country string, limit int) ([]models.SearchHit, error) {
	return models.TextSearchWarehouses(ctx, text, country, limit)
}
