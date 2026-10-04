package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Map returns every matching pin (D-077). The unfiltered country view is
// cached per catalogVersion (bumped on publish / unpublish) and carries an
// ETag. With a location, pins are limited to the ring a search would use.
// The admin map is never cached and adds each point's id and status.
func (s *svc) Map(ctx context.Context, f *domain.SearchFilters, country string, admin bool) (dto.MapResponse, string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "IN"
	}
	if f == nil && !admin {
		return s.countryMap(ctx, country)
	}
	if f == nil {
		f = &domain.SearchFilters{}
	}
	if f.Location == nil {
		f.Location = &domain.SearchLocation{Country: country}
	} else if f.Location.Country == "" {
		f.Location.Country = country
	}
	n, q, _, err := normalize(s.rules.Snapshot(), *f, s.cfg(), admin)
	if err != nil {
		return dto.MapResponse{}, "", err
	}
	var resp domain.SearchResponse
	near := s.locate(ctx, &n, &resp)
	var maxM float64
	if near != nil {
		radius, err := s.chooseRadius(ctx, *near, q, n)
		if err != nil {
			return dto.MapResponse{}, "", err
		}
		maxM = float64(radius.UsedKm) * 1000
	}
	pts, err := s.store.MapPoints(ctx, q, near, maxM)
	if err != nil {
		return dto.MapResponse{}, "", err
	}
	out := buildMap(pts)
	if admin {
		out.AdminPoints = make([]dto.AdminMapPoint, 0, len(pts))
		for _, p := range pts {
			out.AdminPoints = append(out.AdminPoints, dto.AdminMapPoint{ID: p.ID.Hex(), Status: p.Status})
		}
	}
	return out, "", nil
}

func (s *svc) countryMap(ctx context.Context, country string) (dto.MapResponse, string, error) {
	v, err := s.store.CatalogVersion(ctx)
	if err != nil {
		return dto.MapResponse{}, "", err
	}
	etag := fmt.Sprintf(`"map-%s-%d"`, country, v)
	s.mapMu.Lock()
	e, ok := s.mapCache[country]
	s.mapMu.Unlock()
	if ok && e.version == v {
		return e.resp, etag, nil
	}
	pts, err := s.store.MapPoints(ctx, models.SearchQuery{Country: country}, nil, 0)
	if err != nil {
		return dto.MapResponse{}, "", err
	}
	resp := buildMap(pts)
	s.mapMu.Lock()
	s.mapCache[country] = mapEntry{version: v, resp: resp}
	s.mapMu.Unlock()
	return resp, etag, nil
}

// buildMap turns pins into compact points with a price band (0 = no
// price, 1–3 = cheapest → dearest third of the priced pins) and a bbox.
func buildMap(pts []models.MapPoint) dto.MapResponse {
	resp := dto.MapResponse{Points: make([][4]any, 0, len(pts)), BBox: []float64{}, Total: len(pts)}
	var prices []float64
	for _, p := range pts {
		if p.Price != nil && p.Price.PerSqmMonth > 0 {
			prices = append(prices, p.Price.PerSqmMonth)
		}
	}
	sort.Float64s(prices)
	band := func(p *models.Price) int {
		if p == nil || p.PerSqmMonth <= 0 || len(prices) == 0 {
			return 0
		}
		i := sort.SearchFloat64s(prices, p.PerSqmMonth)
		return min(3*i/len(prices)+1, 3)
	}
	bbox := []float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		lng, lat := p.Loc.Coordinates[0], p.Loc.Coordinates[1]
		resp.Points = append(resp.Points, [4]any{p.ShortID, lat, lng, band(p.Price)})
		bbox[0], bbox[1] = math.Min(bbox[0], lng), math.Min(bbox[1], lat)
		bbox[2], bbox[3] = math.Max(bbox[2], lng), math.Max(bbox[3], lat)
	}
	if len(pts) > 0 {
		resp.BBox = bbox
	}
	return resp
}
