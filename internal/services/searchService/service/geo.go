package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/services/searchService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// placeLookupLimit bounds the pincode rows read for one place name.
const (
	placeLookupLimit    = 50
	districtLookupLimit = 500
)

// resolved is a geocoded search location.
type resolved struct {
	Point  domain.LatLng
	Label  string
	Source string
}

// geoResolver resolves a typed location (D-078): geocode_cache → Google
// (with a circuit breaker) → the pincodes table. Only Google answers are
// cached, so a pincode fallback during an outage isn't kept for weeks.
type geoResolver struct {
	store    store.Store
	geocoder func() interfaces.Geocoder
	cfg      func() config.SearchValues
	now      func() time.Time

	mu        sync.Mutex
	fails     int
	openUntil time.Time
}

// resolve returns the point for loc. ErrNotFound when every source answered
// "nothing there"; ErrGeoUnavailable when a source failed and none answered.
func (g *geoResolver) resolve(ctx context.Context, loc domain.SearchLocation) (resolved, error) {
	if p := loc.Point; p != nil {
		return resolved{Point: *p, Source: domain.GeoSourcePoint}, nil
	}
	kind, query := "place", strings.ToLower(loc.Place)
	if loc.PostalCode != "" {
		kind, query = "pin", loc.PostalCode
	}
	if query == "" {
		return resolved{}, searchService.ErrNotFound
	}
	key := loc.Country + "|" + kind + "|" + query
	now := g.now().UTC()

	if e, err := g.store.GetGeocode(ctx, key, now); err == nil {
		return resolved{Point: domain.LatLng{Lat: e.Lat, Lng: e.Lng}, Label: e.Label, Source: domain.GeoSourceCache}, nil
	} else if !errors.Is(err, models.ErrNotFound) {
		log.Warn("search: geocode cache read failed", "error", err)
	}

	failed := false
	if r, err := g.google(ctx, loc, kind); err == nil {
		ttl := time.Duration(max(g.cfg().GeocodeCacheHours, 1)) * time.Hour
		e := models.GeocodeCacheEntry{Key: key, Lat: r.Point.Lat, Lng: r.Point.Lng, Label: r.Label, Source: r.Source, ExpiresAt: now.Add(ttl)}
		if err := g.store.PutGeocode(context.WithoutCancel(ctx), e); err != nil {
			log.Warn("search: geocode cache write failed", "error", err)
		}
		return r, nil
	} else if !errors.Is(err, interfaces.ErrGeocodeNotFound) {
		failed = true
	}

	if loc.Country == "IN" {
		r, err := g.pincode(ctx, kind, query)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, models.ErrNotFound) {
			log.Warn("search: pincode lookup failed", "error", err)
			failed = true
		}
	}
	if failed {
		return resolved{}, searchService.ErrGeoUnavailable
	}
	return resolved{}, searchService.ErrNotFound
}

// google asks the geocoder unless the breaker is open. A "not found" answer
// counts as a success for the breaker.
func (g *geoResolver) google(ctx context.Context, loc domain.SearchLocation, kind string) (resolved, error) {
	gc := g.geocoder()
	if gc == nil {
		return resolved{}, errors.New("no geocoder configured")
	}
	if !g.allow() {
		return resolved{}, errors.New("geocoder circuit open")
	}
	address := loc.Place
	if kind == "pin" {
		address = loc.PostalCode
	}
	res, err := gc.Geocode(ctx, address, loc.Country)
	g.report(err == nil || errors.Is(err, interfaces.ErrGeocodeNotFound))
	if err != nil {
		return resolved{}, err
	}
	label := res.Formatted
	if label == "" {
		label = address
	}
	return resolved{Point: domain.LatLng{Lat: res.Lat, Lng: res.Lng}, Label: label, Source: domain.GeoSourceGoogle}, nil
}

func (g *geoResolver) allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.now().Before(g.openUntil)
}

// report counts consecutive failures; reaching the threshold opens the
// breaker. After the open window one call is let through (half-open): a
// failure reopens it at once.
func (g *geoResolver) report(ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ok {
		g.fails = 0
		return
	}
	g.fails++
	cfg := g.cfg()
	if threshold := max(cfg.BreakerFailures, 1); g.fails >= threshold {
		g.openUntil = g.now().Add(time.Duration(max(cfg.BreakerOpenSeconds, 1)) * time.Second)
		log.Warn("search: geocoder circuit opened", "failures", g.fails)
	}
}

// pincode resolves from the GeoNames table: a postal code directly; a place
// name when it names one pincode, several in one district, or a district
// (centroid).
func (g *geoResolver) pincode(ctx context.Context, kind, query string) (resolved, error) {
	if kind == "pin" {
		p, err := g.store.Pincode(ctx, query)
		if err != nil {
			return resolved{}, err
		}
		return resolved{Point: domain.LatLng{Lat: p.Lat, Lng: p.Lng}, Label: pinLabel(p), Source: domain.GeoSourcePincode}, nil
	}
	ps, err := g.store.PincodesByPlace(ctx, query, placeLookupLimit)
	if err != nil {
		return resolved{}, err
	}
	if len(ps) > 0 && sameDistrict(ps) {
		label := ps[0].Place
		if len(ps) > 1 {
			label = titleCase(query) + ", " + ps[0].District
		}
		return resolved{Point: centroid(ps), Label: label, Source: domain.GeoSourcePincode}, nil
	}
	ps, err = g.store.PincodesByDistrict(ctx, query, districtLookupLimit)
	if err != nil {
		return resolved{}, err
	}
	if len(ps) == 0 {
		return resolved{}, models.ErrNotFound
	}
	label := ps[0].District
	if ps[0].State != "" {
		label += ", " + ps[0].State
	}
	return resolved{Point: centroid(ps), Label: label, Source: domain.GeoSourcePincode}, nil
}

func pinLabel(p *models.Pincode) string {
	parts := []string{p.Code}
	for _, s := range []string{p.Place, p.District} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

func sameDistrict(ps []models.Pincode) bool {
	for _, p := range ps[1:] {
		if p.DistrictLC != ps[0].DistrictLC {
			return false
		}
	}
	return true
}

func centroid(ps []models.Pincode) domain.LatLng {
	var c domain.LatLng
	for _, p := range ps {
		c.Lat += p.Lat
		c.Lng += p.Lng
	}
	c.Lat /= float64(len(ps))
	c.Lng /= float64(len(ps))
	return c
}

func titleCase(s string) string {
	ws := strings.Fields(s)
	for i, w := range ws {
		ws[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(ws, " ")
}
