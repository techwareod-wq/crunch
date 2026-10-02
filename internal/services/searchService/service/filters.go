package service

import (
	"slices"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var sorts = []string{domain.SortRelevance, domain.SortDistance, domain.SortPriceAsc, domain.SortPriceDesc, domain.SortAreaDesc}

// normalize validates f against the tree and config and returns the
// filters as applied, the base query and the dropped keys. Unknown chip /
// range / industry keys are dropped (spec 04 Validation); malformed values
// are a ValidationError.
func normalize(snap *domain.Snapshot, f domain.SearchFilters, cfg config.SearchValues) (domain.SearchFilters, models.SearchQuery, []string, error) {
	n := f
	n.SessionID = "" // analytics only (Viewer.SessionID)
	dropped := []string{}
	country := "IN"
	if f.Location != nil {
		loc := *f.Location
		loc.Country = strings.ToUpper(strings.TrimSpace(loc.Country))
		if loc.Country != "" {
			country = loc.Country
		}
		loc.Country = country
		loc.PostalCode = strings.ReplaceAll(strings.TrimSpace(loc.PostalCode), " ", "")
		loc.Place = strings.Join(strings.Fields(loc.Place), " ")
		if p := loc.Point; p != nil && (p.Lat < -90 || p.Lat > 90 || p.Lng < -180 || p.Lng > 180) {
			return n, models.SearchQuery{}, nil, searchService.Invalidf("location.point is out of range")
		}
		n.Location = &loc
		if loc.Point == nil && loc.PostalCode == "" && loc.Place == "" {
			n.Location = nil
		}
	}
	q := models.SearchQuery{Country: country, IncludeUnverified: f.IncludeUnverified}

	// Radius: default per country, capped at the last ring.
	steps := radiusSteps(cfg)
	if n.RadiusKm < 0 {
		return n, q, nil, searchService.Invalidf("radiusKm must be positive")
	}
	if n.RadiusKm == 0 {
		n.RadiusKm = cfg.RadiusFor(country)
	}
	n.RadiusKm = min(n.RadiusKm, steps[len(steps)-1])
	if n.AutoExpand == nil {
		yes := true
		n.AutoExpand = &yes
	}

	// Paging + sort.
	if n.Page < 0 || n.Limit < 0 {
		return n, q, nil, searchService.Invalidf("page and limit must be positive")
	}
	n.Page = max(n.Page, 1)
	if n.Limit == 0 {
		n.Limit = cfg.PageLimitDefault
	}
	if n.Limit <= 0 {
		n.Limit = 20
	}
	if lim := cfg.PageLimitMax; lim > 0 {
		n.Limit = min(n.Limit, lim)
	}
	if n.Sort == "" {
		n.Sort = domain.SortRelevance
		if n.Location != nil {
			n.Sort = domain.SortDistance
		}
	}
	if !slices.Contains(sorts, n.Sort) {
		return n, q, nil, searchService.Invalidf("sort must be one of %s", strings.Join(sorts, ", "))
	}

	// Area + price.
	if a := n.AreaSqm; a != nil {
		if err := checkMinMax("areaSqm", *a, false); err != nil {
			return n, q, nil, err
		}
		q.AreaMin, q.AreaMax = a.Min, a.Max
	}
	if p := n.Price; p != nil {
		cp := *p
		cp.Currency = strings.ToUpper(strings.TrimSpace(cp.Currency))
		if cp.Currency == "" {
			cp.Currency = cfg.CurrencyFor(country)
		}
		if err := checkMinMax("price", domain.MinMax{Min: cp.PerSqmMonthMin, Max: cp.PerSqmMonthMax}, false); err != nil {
			return n, q, nil, err
		}
		n.Price = &cp
		q.PriceCurrency, q.PriceMin, q.PriceMax = cp.Currency, cp.PerSqmMonthMin, cp.PerSqmMonthMax
	}

	// Industries (all must pass, D-037).
	n.Industries = nil
	for _, k := range f.Industries {
		k = strings.TrimSpace(k)
		if _, ok := snap.Industry(k); !ok {
			dropped = append(dropped, "industry:"+k)
			continue
		}
		if !slices.Contains(n.Industries, k) {
			n.Industries = append(n.Industries, k)
		}
	}
	q.Industries = n.Industries

	// Chips (AND, D-072).
	n.Chips = nil
	for _, c := range f.Chips {
		c = strings.TrimSpace(c)
		unk, ok := chipUnk(snap, c)
		if !ok {
			dropped = append(dropped, "chip:"+c)
			continue
		}
		if !slices.Contains(n.Chips, c) {
			n.Chips = append(n.Chips, c)
			q.Chips = append(q.Chips, models.SearchChip{Chip: c, Unk: unk})
		}
	}

	// Ranges.
	n.Ranges = nil
	keys := make([]string, 0, len(f.Ranges))
	for k := range f.Ranges {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		mm := f.Ranges[k]
		if mm.Empty() {
			continue
		}
		if err := checkMinMax("ranges."+k, mm, true); err != nil {
			return n, q, nil, err
		}
		r, ok := rangeQuery(snap, k, mm)
		if !ok {
			dropped = append(dropped, "range:"+k)
			continue
		}
		if n.Ranges == nil {
			n.Ranges = map[string]domain.MinMax{}
		}
		n.Ranges[k] = mm
		q.Ranges = append(q.Ranges, r)
	}
	return n, q, dropped, nil
}

// checkMinMax: min ≤ max; area and price can't be negative (attribute
// ranges can: temperatures).
func checkMinMax(name string, m domain.MinMax, allowNegative bool) error {
	if !allowNegative && ((m.Min != nil && *m.Min < 0) || (m.Max != nil && *m.Max < 0)) {
		return searchService.Invalidf("%s can't be negative", name)
	}
	if m.Min != nil && m.Max != nil && *m.Min > *m.Max {
		return searchService.Invalidf("%s: min is above max", name)
	}
	return nil
}

// chipUnk checks a chip against the tree (public + filterable, spec 02
// Projection format) and returns the `unk` keys meaning "unknown" for it:
// the field path (if any) and the node.
func chipUnk(snap *domain.Snapshot, chip string) ([]string, bool) {
	path, opt, hasOpt := strings.Cut(chip, ":")
	nodeKey, fieldKey, isField := strings.Cut(path, ".")
	n, ok := snap.Node(nodeKey)
	if !ok || n.Key == domain.RootKey || !n.Public {
		return nil, false
	}
	if !isField {
		return []string{nodeKey}, !hasOpt && n.Filterable
	}
	f, ok := n.Field(fieldKey)
	if !ok || !f.Public || !f.Filterable {
		return nil, false
	}
	switch {
	case f.Type == domain.TypeBool && !hasOpt:
	case (f.Type == domain.TypePick || f.Type == domain.TypeMulti) && hasOpt && f.HasOption(opt):
	default:
		return nil, false
	}
	return []string{path, nodeKey}, true
}

// rangeQuery maps a range filter on a numeric field to `nums` conditions.
// A range-typed field stores <path>_min / <path>_max: min ⇒ its max ≥ min,
// max ⇒ its min ≤ max ("can reach", 02).
func rangeQuery(snap *domain.Snapshot, path string, mm domain.MinMax) (models.SearchRange, bool) {
	n, f, ok := snap.Field(path)
	if !ok || n.Key == domain.RootKey || !n.Public || !f.Public || !f.Filterable {
		return models.SearchRange{}, false
	}
	r := models.SearchRange{Unk: []string{path, n.Key}}
	switch f.Type {
	case domain.TypeNumber, domain.TypeArea, domain.TypeRatio:
		r.Conds = []models.NumCond{{K: path, Gte: mm.Min, Lte: mm.Max}}
	case domain.TypeRange:
		if mm.Min != nil {
			r.Conds = append(r.Conds, models.NumCond{K: path + "_max", Gte: mm.Min})
		}
		if mm.Max != nil {
			r.Conds = append(r.Conds, models.NumCond{K: path + "_min", Lte: mm.Max})
		}
	default:
		return models.SearchRange{}, false
	}
	return r, true
}

// radiusSteps returns the configured rings (km, ascending, > 0).
func radiusSteps(cfg config.SearchValues) []int {
	var out []int
	for _, s := range cfg.RadiusSteps {
		if s > 0 && (len(out) == 0 || s > out[len(out)-1]) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []int{25, 50, 100, 250}
	}
	return out
}
