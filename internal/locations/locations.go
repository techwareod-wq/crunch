// Package locations is the app-wide DataForSEO country catalog: one boot-time
// fetch shared by every consumer that maps between country names, ISO codes,
// and DataForSEO location codes (onboarding validation, audit market
// resolution). Before this existed each consumer either loaded its own copy
// or hand-rolled a subset map.
package locations

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// Country is one Country-type DataForSEO location.
type Country struct {
	Name         string
	LocationCode int
	// ISOCode is ISO 3166-1 alpha-2, as delivered by DataForSEO.
	ISOCode string
}

// Catalog is the immutable lookup structure. Built once at boot; all reads
// are lock-free. Every method is nil-receiver-safe and degrades to "unknown"
// so consumers without a catalog (tests, partial wiring) fail soft.
type Catalog struct {
	byName  map[string]Country // lowercased location_name
	byISO   map[string]Country // lowercased ISO alpha-2
	byCode  map[int]Country
	ordered []Country // catalog order (the onboarding dropdown contract)
}

// Load fetches the DataForSEO locations catalog and builds the country
// indexes. The caller decides how fatal a failure is (boot wiring treats it
// as a startup error — locations are critical for onboarding validation and
// every location-scoped SERP call).
func Load(ctx context.Context, dfs interfaces.DataForSEO) (*Catalog, error) {
	resp, err := dfs.GetLocations(ctx)
	if err != nil {
		return nil, fmt.Errorf("locations: fetch DataForSEO catalog: %w", err)
	}
	if resp == nil || len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return nil, fmt.Errorf("locations: DataForSEO locations response empty")
	}
	var countries []Country
	for _, loc := range resp.Tasks[0].Result {
		if loc.LocationType != "Country" {
			continue
		}
		countries = append(countries, Country{
			Name:         loc.LocationName,
			LocationCode: loc.LocationCode,
			ISOCode:      loc.CountryISOCode,
		})
	}
	return NewCatalog(countries)
}

// NewCatalog builds a catalog from an explicit country list (Load's tail;
// exported for tests and any future non-DataForSEO source).
func NewCatalog(countries []Country) (*Catalog, error) {
	if len(countries) == 0 {
		return nil, fmt.Errorf("locations: no Country-type locations")
	}
	c := &Catalog{
		byName:  make(map[string]Country, len(countries)),
		byISO:   make(map[string]Country, len(countries)),
		byCode:  make(map[int]Country, len(countries)),
		ordered: countries,
	}
	for _, ct := range countries {
		c.byName[strings.ToLower(ct.Name)] = ct
		if iso := strings.ToLower(strings.TrimSpace(ct.ISOCode)); iso != "" {
			c.byISO[iso] = ct
		}
		if ct.LocationCode > 0 {
			c.byCode[ct.LocationCode] = ct
		}
	}
	return c, nil
}

// ByName resolves a country by display name (case/whitespace-insensitive).
func (c *Catalog) ByName(name string) (Country, bool) {
	if c == nil {
		return Country{}, false
	}
	ct, ok := c.byName[strings.ToLower(strings.TrimSpace(name))]
	return ct, ok
}

// CodeForISO maps an ISO 3166-1 alpha-2 code to its DataForSEO location code
// (0 = unknown).
func (c *Catalog) CodeForISO(iso string) int {
	if c == nil {
		return 0
	}
	return c.byISO[strings.ToLower(strings.TrimSpace(iso))].LocationCode
}

// NameForCode maps a DataForSEO location code to its display name ("" =
// unknown — callers omit the label rather than guess).
func (c *Catalog) NameForCode(code int) string {
	if c == nil {
		return ""
	}
	return c.byCode[code].Name
}

// Countries returns the catalog-ordered country list. Callers must not
// mutate it.
func (c *Catalog) Countries() []Country {
	if c == nil {
		return nil
	}
	return c.ordered
}
