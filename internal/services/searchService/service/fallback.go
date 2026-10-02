package service

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// earthRadiusM is the mean Earth radius (haversine).
const earthRadiusM = 6371008.8

func (s *svc) Similar(ctx context.Context, vec []float32, q searchService.FallbackQuery) ([]domain.SearchCard, error) {
	limit := fallbackLimit(q)
	hits, err := s.store.Vector(ctx, q.VectorIndex, vec, max(q.NumCandidates, limit), limit+len(q.Exclude), countryOf(q))
	if err != nil {
		return nil, err
	}
	return s.fallbackCards(hits, q, limit), nil
}

func (s *svc) Keyword(ctx context.Context, text string, q searchService.FallbackQuery) ([]domain.SearchCard, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return []domain.SearchCard{}, nil
	}
	limit := fallbackLimit(q)
	// Over-fetch: the distance and exclude filters run here, after $text.
	hits, err := s.store.Text(ctx, text, countryOf(q), (limit+len(q.Exclude))*3)
	if err != nil {
		return nil, err
	}
	return s.fallbackCards(hits, q, limit), nil
}

func fallbackLimit(q searchService.FallbackQuery) int {
	if q.Limit > 0 {
		return q.Limit
	}
	return 10
}

func countryOf(q searchService.FallbackQuery) string {
	if c := strings.ToUpper(strings.TrimSpace(q.Country)); c != "" {
		return c
	}
	return "IN"
}

// fallbackCards drops excluded and too-distant hits (beyond the last
// radius ring) and builds cards, keeping the store's order.
func (s *svc) fallbackCards(hits []models.SearchHit, q searchService.FallbackQuery, limit int) []domain.SearchCard {
	steps := radiusSteps(s.cfg())
	maxM := float64(steps[len(steps)-1]) * 1000
	out := []domain.SearchCard{}
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if slices.Contains(q.Exclude, h.ShortID) {
			continue
		}
		if q.Near != nil {
			if h.Loc == nil {
				continue
			}
			d := haversineM(*q.Near, h.Loc.Coordinates[1], h.Loc.Coordinates[0])
			if d > maxM {
				continue
			}
			h.DistM = &d
		}
		out = append(out, s.card(h, domain.SearchFilters{}, models.SearchQuery{}))
	}
	return out
}

func haversineM(a domain.LatLng, lat, lng float64) float64 {
	rad := math.Pi / 180
	dLat, dLng := (lat-a.Lat)*rad, (lng-a.Lng)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(a.Lat*rad)*math.Cos(lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}
