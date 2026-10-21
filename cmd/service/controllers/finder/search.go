package finder

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Filter search for the experimental finder page. Every filter is hard
// (AND). The handler prefilters in Mongo with mongoFilter; keep is the
// source of truth for place / radius / hubs and is what the tests cover.

const (
	sqftPerSqm = 10.764
	// minAreaRatio: a listing must offer at least 80% of the asked area.
	minAreaRatio = 0.8
	// sqftPerPallet sizes an area from a pallet count.
	sqftPerPallet = 12

	// The map shows every pin, so the limit is generous.
	defaultLimit = 100
	maxLimit     = 500

	typeChipPrefix = "warehouse.type:"
)

// Sorts.
const (
	sortDistance = "distance"
	sortArea     = "area"
	sortRate     = "rate"
	// sortCompleteness is internal: "distance" without a place (no
	// centroid) orders by completeness desc, then name.
	sortCompleteness = "completeness"
)

var typeKeys = map[string]bool{"general": true, "bonded": true, "ftwz": true}

// radii are the accepted radiusKm values (0 = same city only).
var radii = []int{0, 50, 100}

// feature is one page filter: the chip it needs (or the pharma fit) and
// the tag shown on cards that have it.
type feature struct {
	Key, Chip, Tag string
}

// pharmaFit marks the feature matched on `fit` instead of a chip.
const pharmaFit = "fit:pharma"

// features in display order. The bool chips are the seed's root fields.
var features = []feature{
	{"dg", "hazmat", "DG capable"},
	{"cold", "cold_storage", "Cold storage"},
	{"food_grade", "warehouse.food_grade", "Food grade"},
	{"open_yard", "warehouse.open_yard", "Open yard"},
	{"racked", "warehouse.racked", "Racked"},
	{"transport", "warehouse.transport_available", "Transport"},
	{"fire_noc", "warehouse.fire_noc", "Fire NOC"},
	{"pharma", pharmaFit, "Pharma fit"},
}

func featureOf(key string) (feature, bool) {
	i := slices.IndexFunc(features, func(f feature) bool { return f.Key == key })
	if i < 0 {
		return feature{}, false
	}
	return features[i], true
}

// searchRequest is the POST /v1/public/finder/search body.
type searchRequest struct {
	Place       string   `json:"place"`
	RadiusKm    int      `json:"radiusKm"`
	AreaSqft    float64  `json:"areaSqft"`
	Pallets     int      `json:"pallets"`
	Type        string   `json:"type"`
	NearPort    bool     `json:"nearPort"`
	NearAirport bool     `json:"nearAirport"`
	NearRail    bool     `json:"nearRail"`
	Features    []string `json:"features"`
	Sort        string   `json:"sort"`
	Limit       int      `json:"limit"`
}

// query is a validated search.
type query struct {
	Place       string // "" = anywhere (no centroid, radius or distance)
	RadiusKm    int
	AreaSqft    float64 // 0 = no area filter
	Type        string  // "" = any
	NearPort    bool
	NearAirport bool
	NearRail    bool
	Chips       []string // every one required
	Pharma      bool
	Sort        string
	Limit       int
}

// parseQuery validates req; a non-empty message is a 400.
func parseQuery(req searchRequest) (query, string) {
	q := query{Place: strings.TrimSpace(req.Place), RadiusKm: req.RadiusKm, AreaSqft: req.AreaSqft,
		Type: strings.ToLower(strings.TrimSpace(req.Type)), NearPort: req.NearPort, NearAirport: req.NearAirport,
		NearRail: req.NearRail, Sort: strings.ToLower(strings.TrimSpace(req.Sort)), Limit: req.Limit}
	switch {
	case !slices.Contains(radii, q.RadiusKm):
		return q, "radiusKm must be 0, 50 or 100"
	case q.AreaSqft < 0 || req.Pallets < 0:
		return q, "areaSqft and pallets can't be negative"
	case q.Type != "" && !typeKeys[q.Type]:
		return q, "type must be general, bonded, ftwz or empty"
	}
	if q.AreaSqft == 0 && req.Pallets > 0 {
		q.AreaSqft = float64(req.Pallets * sqftPerPallet)
	}
	if q.Type != "" {
		q.Chips = append(q.Chips, typeChipPrefix+q.Type)
	}
	for _, key := range req.Features {
		f, ok := featureOf(strings.ToLower(strings.TrimSpace(key)))
		if !ok {
			return q, "unknown feature " + `"` + key + `"`
		}
		switch {
		case f.Chip == pharmaFit:
			q.Pharma = true
		case !slices.Contains(q.Chips, f.Chip):
			q.Chips = append(q.Chips, f.Chip)
		}
	}
	switch q.Sort {
	case "":
		q.Sort = sortDistance
	case sortDistance, sortArea, sortRate:
	default:
		return q, "sort must be distance, area or rate"
	}
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	q.Limit = min(q.Limit, maxLimit)
	return q, ""
}

// minSqm is the smallest total area that passes the area filter.
func (q query) minSqm() float64 { return minAreaRatio * q.AreaSqft / sqftPerSqm }

// sortMode is the order to apply: without a place there is no distance, so
// "distance" falls back to completeness then name.
func (q query) sortMode() string {
	if q.Place == "" && q.Sort == sortDistance {
		return sortCompleteness
	}
	return q.Sort
}

// cityFilter matches q.Place, case-insensitive and exact.
func cityFilter(place string) bson.M {
	return bson.M{"$regex": "^" + regexp.QuoteMeta(place) + "$", "$options": "i"}
}

// point is a WGS84 lat/lng.
type point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func pointOf(g *models.GeoPoint) *point {
	if g == nil {
		return nil
	}
	return &point{Lat: g.Coordinates[1], Lng: g.Coordinates[0]}
}

// centroidOf averages the pins (nil when there are none).
func centroidOf(locs []*models.GeoPoint) *point {
	var sum point
	n := 0
	for _, g := range locs {
		if p := pointOf(g); p != nil {
			sum.Lat, sum.Lng = sum.Lat+p.Lat, sum.Lng+p.Lng
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &point{Lat: sum.Lat / float64(n), Lng: sum.Lng / float64(n)}
}

// mongoFilter is the indexed prefilter: live, area, required chips, pharma
// fit, and the place when given (the city, or within the radius of the
// centroid).
func mongoFilter(q query, c *point) bson.M {
	f := bson.M{"status": models.WarehouseLive}
	if q.AreaSqft > 0 {
		f["total_sqm"] = bson.M{"$gte": q.minSqm()}
	}
	if len(q.Chips) > 0 {
		f["chips"] = bson.M{"$all": q.Chips}
	}
	if q.Pharma {
		f["fit"] = bson.M{"$in": bson.A{"pharma:" + string(domain.VerdictFit), "pharma:" + string(domain.VerdictPartial)}}
	}
	if q.Place == "" {
		return f
	}
	place := bson.A{bson.M{"city": cityFilter(q.Place)}}
	if q.RadiusKm > 0 && c != nil {
		place = append(place, bson.M{"loc": bson.M{"$geoWithin": bson.M{
			"$centerSphere": bson.A{bson.A{c.Lng, c.Lat}, float64(q.RadiusKm) / earthKm}}}})
	}
	f["$or"] = place
	return f
}

// candidate is one live listing as read for the finder.
type candidate struct {
	ShortID      string           `bson:"short_id"`
	Slug         string           `bson:"slug"`
	Name         string           `bson:"name"`
	City         string           `bson:"city"`
	Locality     string           `bson:"locality"`
	Loc          *models.GeoPoint `bson:"loc"`
	TotalSqm     float64          `bson:"total_sqm"`
	Price        *models.Price    `bson:"price"`
	CoverKey     string           `bson:"cover_key"`
	Chips        []string         `bson:"chips"`
	Fit          []string         `bson:"fit"`
	Nums         []models.NumFact `bson:"nums"`
	Completeness float64          `bson:"completeness"`
	Live         *struct {
		Attributes struct {
			Warehouse struct {
				Fields struct {
					Rent *models.FieldValue `bson:"rent"`
				} `bson:"fields"`
			} `bson:"warehouse"`
			Building struct {
				Fields struct {
					ClearHeight *models.FieldValue `bson:"clear_height"`
				} `bson:"fields"`
			} `bson:"building"`
		} `bson:"attributes"`
	} `bson:"live"`
}

// candidateProjection fetches only what keep / sort / the card read.
var candidateProjection = bson.M{
	"short_id": 1, "slug": 1, "name": 1, "city": 1, "locality": 1, "loc": 1, "total_sqm": 1, "price": 1,
	"cover_key": 1, "chips": 1, "fit": 1, "nums": 1, "completeness": 1,
	"live.attributes.warehouse.fields.rent":        1,
	"live.attributes.building.fields.clear_height": 1,
}

// hit is a candidate that passed every filter.
type hit struct {
	C                               candidate
	DistKm                          *float64
	NearPort, NearAirport, NearRail bool
}

// keep applies every hard filter to c (the Mongo prefilter is only an
// optimisation): place / radius around centroid (skipped without a place),
// area, chips, pharma fit and the near-hub flags. A listing without a pin
// passes a place only through its city and never counts as near a hub.
func keep(q query, centroid *point, c candidate) (hit, bool) {
	h := hit{C: c}
	p := pointOf(c.Loc)
	if p != nil {
		if centroid != nil {
			d := haversineKm(centroid.Lat, centroid.Lng, p.Lat, p.Lng)
			h.DistKm = &d
		}
		h.NearPort = nearAny(p.Lat, p.Lng, ports, nearPortKm)
		h.NearAirport = nearAny(p.Lat, p.Lng, airports, nearAirportKm)
		h.NearRail = nearAny(p.Lat, p.Lng, rail, nearRailKm)
	}
	inCity := strings.EqualFold(strings.TrimSpace(c.City), q.Place)
	inRadius := q.RadiusKm > 0 && h.DistKm != nil && *h.DistKm <= float64(q.RadiusKm)
	switch {
	case q.Place != "" && !inCity && !inRadius:
		return h, false
	case q.AreaSqft > 0 && c.TotalSqm*sqftPerSqm < minAreaRatio*q.AreaSqft:
		return h, false
	case slices.ContainsFunc(q.Chips, func(chip string) bool { return !slices.Contains(c.Chips, chip) }):
		return h, false
	case q.Pharma && !hasPharmaFit(c.Fit):
		return h, false
	case q.NearPort && !h.NearPort, q.NearAirport && !h.NearAirport, q.NearRail && !h.NearRail:
		return h, false
	}
	return h, true
}

func hasPharmaFit(fit []string) bool {
	return slices.Contains(fit, "pharma:"+string(domain.VerdictFit)) || slices.Contains(fit, "pharma:"+string(domain.VerdictPartial))
}

// sortHits orders hs by mode: distance asc (no pin last, then completeness
// desc), area desc, rate asc (on request / unpriced last), or completeness
// desc then name (distance without a place). Short id breaks the remaining
// ties so the order is stable.
func sortHits(hs []hit, mode string) {
	slices.SortStableFunc(hs, func(a, b hit) int {
		var c int
		switch mode {
		case sortArea:
			c = cmp.Compare(b.C.TotalSqm, a.C.TotalSqm)
		case sortRate:
			ra, oka := rateOf(a.C)
			rb, okb := rateOf(b.C)
			c = cmp.Or(boolLast(oka, okb), cmp.Compare(ra, rb))
		case sortCompleteness:
			c = cmp.Or(cmp.Compare(b.C.Completeness, a.C.Completeness), strings.Compare(a.C.Name, b.C.Name))
		default:
			c = cmp.Or(boolLast(a.DistKm != nil, b.DistKm != nil), cmp.Compare(deref(a.DistKm), deref(b.DistKm)),
				cmp.Compare(b.C.Completeness, a.C.Completeness))
		}
		return cmp.Or(c, strings.Compare(a.C.ShortID, b.C.ShortID))
	})
}

// rateOf is the normalized price per sq m per month; false = on request or
// unpriced.
func rateOf(c candidate) (float64, bool) {
	if c.Price == nil || c.Price.OnRequest || c.Price.PerSqmMonth <= 0 {
		return 0, false
	}
	return c.Price.PerSqmMonth, true
}

// boolLast orders true before false.
func boolLast(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// tagsOf labels every feature c has, in display order.
func tagsOf(c candidate) []string {
	tags := []string{}
	for _, f := range features {
		if (f.Chip == pharmaFit && hasPharmaFit(c.Fit)) || slices.Contains(c.Chips, f.Chip) {
			tags = append(tags, f.Tag)
		}
	}
	return tags
}

// typeOf is the listing's warehouse.type option ("" = none).
func typeOf(chips []string) string {
	for _, c := range chips {
		if t, ok := strings.CutPrefix(c, typeChipPrefix); ok {
			return t
		}
	}
	return ""
}

// numOf returns nums key k (nil when absent, never 0 for missing).
func numOf(nums []models.NumFact, k string) *float64 {
	for _, n := range nums {
		if n.K == k {
			v := n.V
			return &v
		}
	}
	return nil
}
