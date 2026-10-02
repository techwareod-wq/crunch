package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/services/searchService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var _ searchService.SearchService = (*svc)(nil)

type svc struct {
	store     store.Store
	rules     domain.Rules
	geo       *geoResolver
	cfg       func() config.SearchValues
	mediaBase func() string
	now       func() time.Time

	logMu  sync.RWMutex
	logger domain.SearchLogger

	mapMu    sync.Mutex
	mapCache map[string]mapEntry // country → unfiltered view
}

type mapEntry struct {
	version int64
	resp    dto.MapResponse
}

// NewService builds the search service. geocoder may be nil (no Google
// key): locations then resolve from the cache and pincode table only.
func NewService(st store.Store, rules domain.Rules, geocoder interfaces.Geocoder, wh config.WarehouseHubValues, aws config.AWSConfig) searchService.SearchService {
	return newService(st, rules, func() interfaces.Geocoder { return geocoder }, func() config.SearchValues { return wh.Search },
		func() string { return aws.PublicMediaBaseURL }, time.Now)
}

func newService(st store.Store, rules domain.Rules, geocoder func() interfaces.Geocoder, cfg func() config.SearchValues, mediaBase func() string, now func() time.Time) *svc {
	return &svc{
		store: st, rules: rules, cfg: cfg, mediaBase: mediaBase, now: now,
		geo:      &geoResolver{store: st, geocoder: geocoder, cfg: cfg, now: now},
		mapCache: map[string]mapEntry{},
	}
}

func (s *svc) SetLogger(l domain.SearchLogger) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logger = l
}

// --- search ---

func (s *svc) Search(ctx context.Context, f domain.SearchFilters, v searchService.Viewer) (domain.SearchResponse, error) {
	cfg := s.cfg()
	n, q, dropped, err := normalize(s.rules.Snapshot(), f, cfg)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	resp := domain.SearchResponse{
		SearchID: primitive.NewObjectID().Hex(),
		Applied:  domain.SearchApplied{Dropped: dropped},
		Results:  []domain.SearchCard{},
		Degraded: []string{},
		Page:     n.Page, Limit: n.Limit,
	}

	near := s.locate(ctx, &n, &resp)
	exec := store.SearchExec{
		Query: q,
		Sort:  models.SearchSort{Mode: n.Sort, Near: near != nil, PriceFilter: n.Price.Active(), Industries: n.Industries},
		Skip:  (n.Page - 1) * n.Limit, Limit: n.Limit,
	}
	if near != nil {
		radius, err := s.chooseRadius(ctx, *near, q, n)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		resp.Radius = radius
		exec.Near, exec.MaxMeters = near, float64(radius.UsedKm)*1000
	}
	res, err := s.store.Search(ctx, exec)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	resp.Applied.Filters = n
	resp.Total = res.Total
	for _, h := range res.Hits {
		resp.Results = append(resp.Results, s.card(h, n, q))
	}
	resp.Facets = facets(res)
	if resp.Radius != nil && resp.Radius.Exhausted && res.Total == 0 {
		resp.Radius.Message = fmt.Sprintf("No warehouses within %d km.", resp.Radius.UsedKm)
	}
	if !v.Quiet {
		s.logSearch(resp, v)
	}
	return resp, nil
}

// locate resolves n.Location. On failure the location is dropped, the sort
// falls back to relevance and the response is marked degraded (D-078).
func (s *svc) locate(ctx context.Context, n *domain.SearchFilters, resp *domain.SearchResponse) *models.GeoPoint {
	if n.Location == nil {
		return nil
	}
	r, err := s.geo.resolve(ctx, *n.Location)
	if err != nil {
		if !errors.Is(err, searchService.ErrNotFound) {
			log.Warn("search: location unresolved", "error", err)
		}
		resp.Degraded = append(resp.Degraded, domain.DegradedGeocode)
		n.Location = nil
		if n.Sort == domain.SortDistance {
			n.Sort = domain.SortRelevance
		}
		return nil
	}
	resp.Applied.ResolvedPoint = &r.Point
	resp.Applied.GeocodeSource = r.Source
	return domain.NewGeoPoint(r.Point.Lat, r.Point.Lng)
}

// chooseRadius counts matches per ring and picks the smallest ring at or
// above the requested radius holding minResults (D-071). Without
// autoExpand only the requested ring is used.
func (s *svc) chooseRadius(ctx context.Context, near models.GeoPoint, q models.SearchQuery, n domain.SearchFilters) (*domain.SearchRadius, error) {
	cfg := s.cfg()
	rings := []int{n.RadiusKm}
	if *n.AutoExpand {
		for _, st := range radiusSteps(cfg) {
			if st > n.RadiusKm {
				rings = append(rings, st)
			}
		}
	}
	ringsM := make([]float64, len(rings))
	for i, r := range rings {
		ringsM[i] = float64(r) * 1000
	}
	counts, err := s.store.RingCounts(ctx, near, q, ringsM)
	if err != nil {
		return nil, err
	}
	return pickRing(rings, counts, max(cfg.MinResults, 1), *n.AutoExpand), nil
}

// pickRing is the ring choice + message (pure, tested).
func pickRing(rings []int, counts []int64, minResults int, autoExpand bool) *domain.SearchRadius {
	r := &domain.SearchRadius{RequestedKm: rings[0], UsedKm: rings[0]}
	if !autoExpand {
		return r
	}
	chosen := len(rings) - 1
	for i, c := range counts {
		if c >= int64(minResults) {
			chosen = i
			break
		}
	}
	r.UsedKm = rings[chosen]
	r.Expanded = chosen > 0
	r.Exhausted = counts[chosen] < int64(minResults)
	r.CountryLink = r.Exhausted
	if r.Expanded {
		if counts[0] == 0 {
			r.Message = fmt.Sprintf("No warehouses within %d km. Showing the nearest within %d km.", r.RequestedKm, r.UsedKm)
		} else {
			r.Message = fmt.Sprintf("Only %d within %d km. Showing the nearest within %d km.", counts[0], r.RequestedKm, r.UsedKm)
		}
	}
	return r
}

// card builds one result card.
func (s *svc) card(h models.SearchHit, n domain.SearchFilters, q models.SearchQuery) domain.SearchCard {
	c := domain.SearchCard{
		ShortID: h.ShortID, Slug: h.Slug, Name: h.Name, City: h.City, Locality: h.Locality,
		TotalSqm: h.TotalSqm, Industries: []string{}, CoverURL: domain.PublicMediaURL(s.mediaBase(), h.CoverKey),
		Rate: rateOf(h), TotalDisplay: areaDisplay(h.Area),
	}
	if h.DistM != nil {
		km := math.Round(*h.DistM/100) / 10
		c.DistKm = &km
	}
	for _, f := range h.Fit {
		ind, verdict, _ := strings.Cut(f, ":")
		if verdict == string(domain.VerdictFit) || verdict == string(domain.VerdictPartial) {
			c.Industries = append(c.Industries, ind)
		}
	}
	c.Unverified = n.IncludeUnverified && !matchedKnown(h, q)
	return c
}

// matchedKnown reports whether h passes every filter on known values alone
// (no include-unverified path).
func matchedKnown(h models.SearchHit, q models.SearchQuery) bool {
	for _, c := range q.Chips {
		if !slices.Contains(h.Chips, c.Chip) {
			return false
		}
	}
	for _, r := range q.Ranges {
		for _, c := range r.Conds {
			if !numHolds(h.Nums, c) {
				return false
			}
		}
	}
	for _, ind := range q.Industries {
		if !slices.Contains(h.Fit, ind+":F") && !slices.Contains(h.Fit, ind+":P") {
			return false
		}
	}
	return true
}

func numHolds(nums []models.NumFact, c models.NumCond) bool {
	for _, n := range nums {
		if n.K == c.K && (c.Gte == nil || n.V >= *c.Gte) && (c.Lte == nil || n.V <= *c.Lte) {
			return true
		}
	}
	return false
}

func rateOf(h models.SearchHit) *domain.PublicRate {
	if h.Rent == nil {
		return nil
	}
	m, ok := domain.DecodeValue[models.Money](h.Rent.V)
	if !ok {
		return nil
	}
	return domain.NewPublicRate(m, h.Price)
}

var unitLabels = map[string]string{domain.UnitSqft: "sq ft", domain.UnitSqm: "sq m"}

// areaDisplay is the total area as entered, e.g. "10,000 sq ft".
func areaDisplay(fv *models.FieldValue) string {
	if fv == nil {
		return ""
	}
	a, ok := domain.DecodeValue[models.Area](fv.V)
	if !ok || a.Value <= 0 {
		return ""
	}
	unit := unitLabels[a.Unit]
	if unit == "" {
		unit = a.Unit
	}
	return groupThousands(a.Value) + " " + unit
}

// groupThousands formats v with comma thousands separators.
func groupThousands(v float64) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if hasFrac {
		b.WriteString("." + frac)
	}
	return b.String()
}

// facets turns the raw facet counts into the response shape.
func facets(res models.SearchResult) domain.SearchFacets {
	f := domain.SearchFacets{Chips: res.ChipCounts, Industries: map[string]domain.IndustryCount{}, Ranges: map[string]domain.Bounds{}}
	for k, n := range res.FitCounts {
		ind, verdict, _ := strings.Cut(k, ":")
		c := f.Industries[ind]
		switch domain.Verdict(verdict) {
		case domain.VerdictFit, domain.VerdictPartial:
			c.Fit += n
		case domain.VerdictUnverified:
			c.Unverified += n
		default:
			continue
		}
		f.Industries[ind] = c
	}
	for k, b := range res.NumStats {
		f.Ranges[k] = domain.Bounds{Min: b[0], Max: b[1]}
	}
	if res.Price != nil {
		f.Price = &domain.Bounds{Min: res.Price[0], Max: res.Price[1]}
	}
	if res.Area != nil {
		f.Area = &domain.Bounds{Min: res.Area[0], Max: res.Area[1]}
	}
	return f
}

// logSearch hands the search to analytics in the background (never blocks
// or fails the response).
func (s *svc) logSearch(resp domain.SearchResponse, v searchService.Viewer) {
	s.logMu.RLock()
	l := s.logger
	s.logMu.RUnlock()
	if l == nil {
		return
	}
	e := domain.SearchEvent{
		SearchID: resp.SearchID, At: s.now().UTC(), Source: "structured", Filters: resp.Applied.Filters,
		Total: resp.Total, Degraded: resp.Degraded, UserID: v.UserID, Staff: v.Staff,
	}
	if resp.Radius != nil {
		e.UsedKm = resp.Radius.UsedKm
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("search: logger panicked", "panic", r)
			}
		}()
		l.Log(context.Background(), e)
	}()
}

// --- nearest (03's 410 page) ---

func (s *svc) Nearest(ctx context.Context, loc models.GeoPoint, limit int, exclude primitive.ObjectID) ([]domain.ListingCard, error) {
	hits, err := s.store.Nearest(ctx, loc, limit, exclude)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ListingCard, 0, len(hits))
	for _, h := range hits {
		out = append(out, domain.ListingCard{
			ShortID: h.ShortID, Slug: h.Slug, Name: h.Name, City: h.City, Locality: h.Locality,
			CoverURL: domain.PublicMediaURL(s.mediaBase(), h.CoverKey), TotalSqm: h.TotalSqm, Rate: rateOf(h),
		})
	}
	return out, nil
}

// --- resolve ---

func (s *svc) Resolve(ctx context.Context, q, country string) (dto.GeoResolved, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "IN"
	}
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return dto.GeoResolved{}, searchService.Invalidf("q is required")
	}
	loc := domain.SearchLocation{Place: q, Country: country}
	if isPostalCode(q) {
		loc = domain.SearchLocation{PostalCode: strings.ReplaceAll(q, " ", ""), Country: country}
	}
	r, err := s.geo.resolve(ctx, loc)
	if err != nil {
		return dto.GeoResolved{}, err
	}
	return dto.GeoResolved{Lat: r.Point.Lat, Lng: r.Point.Lng, Label: r.Label, Source: r.Source}, nil
}

// isPostalCode: digits only (spaces allowed), 3–10 of them.
func isPostalCode(q string) bool {
	d := strings.ReplaceAll(q, " ", "")
	if len(d) < 3 || len(d) > 10 {
		return false
	}
	for _, r := range d {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
