package finder

import (
	"math"
	"net/http"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// collWarehouses is the listings collection (the name is unexported in
// models). The finder only ever reads it.
const collWarehouses = "warehouses"

// measure is a number with its unit, as the editor entered it.
type measure struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// searchCard is one result; shared fields keep the search card's names and
// shapes. Missing numbers are null.
type searchCard struct {
	ShortID        string             `json:"shortId"`
	Slug           string             `json:"slug"`
	Name           string             `json:"name"`
	City           string             `json:"city"`
	Locality       string             `json:"locality"`
	Loc            *point             `json:"loc"`
	DistKm         *float64           `json:"distKm"`
	TotalSqm       float64            `json:"totalSqm"`
	TotalSqft      float64            `json:"totalSqft"`
	Rate           *domain.PublicRate `json:"rate"`
	CoverURL       string             `json:"coverUrl"`
	Type           string             `json:"type"`
	ClearHeight    *measure           `json:"clearHeight"`
	DockDoors      *float64           `json:"dockDoors"`
	HoldCapacityMt *float64           `json:"holdCapacityMt"`
	Tags           []string           `json:"tags"`
	NearPort       bool               `json:"nearPort"`
	NearAirport    bool               `json:"nearAirport"`
	NearRail       bool               `json:"nearRail"`
}

type searchResponse struct {
	Total     int          `json:"total"`
	TotalSqft float64      `json:"totalSqft"`
	Centroid  *point       `json:"centroid"`
	Message   string       `json:"message,omitempty"`
	Results   []searchCard `json:"results"`
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: msg, ErrCode: "invalid"})
}

func internalErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	middleware.GetLogger(r).Error(msg, "error", err)
	middleware.SendJSONError(w, r, apperrors.ErrInternal)
}

// HandleCities lists the cities that have live listings, sorted.
func HandleCities(w http.ResponseWriter, r *http.Request) {
	raw, err := models.Collection(collWarehouses).Distinct(r.Context(), "city", bson.M{"status": models.WarehouseLive})
	if err != nil {
		internalErr(w, r, "finder: list cities failed", err)
		return
	}
	cities := []string{}
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			cities = append(cities, s)
		}
	}
	sort.Strings(cities)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string][]string{"cities": cities})
}

// HandleSearch runs the filter search: the place's centroid (when a place is
// given), then every live listing passing all filters, sorted and limited.
// total and totalSqft cover every match, before the limit.
func HandleSearch(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(searchRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	q, msg := parseQuery(req)
	if msg != "" {
		badRequest(w, r, msg)
		return
	}
	coll := models.Collection(collWarehouses)
	resp := searchResponse{Results: []searchCard{}}

	// 1. Centroid of the place's live listings (no place = anywhere: no
	// centroid, no radius, distKm null).
	if q.Place != "" {
		cur, err := coll.Find(r.Context(), bson.M{"status": models.WarehouseLive, "city": cityFilter(q.Place)},
			options.Find().SetProjection(bson.M{"loc": 1}))
		if err != nil {
			internalErr(w, r, "finder: place lookup failed", err)
			return
		}
		var inCity []struct {
			Loc *models.GeoPoint `bson:"loc"`
		}
		if err := cur.All(r.Context(), &inCity); err != nil {
			internalErr(w, r, "finder: place decode failed", err)
			return
		}
		if len(inCity) == 0 {
			resp.Message = `We don't have live warehouses in "` + q.Place + `" yet.`
			middleware.SendJSONResponse(w, r, http.StatusOK, resp)
			return
		}
		locs := make([]*models.GeoPoint, len(inCity))
		for i, c := range inCity {
			locs[i] = c.Loc
		}
		resp.Centroid = centroidOf(locs)
	}

	// 2. Candidates (indexed prefilter), then every filter in Go.
	cur, err := coll.Find(r.Context(), mongoFilter(q, resp.Centroid), options.Find().SetProjection(candidateProjection))
	if err != nil {
		internalErr(w, r, "finder: search query failed", err)
		return
	}
	var cs []candidate
	if err := cur.All(r.Context(), &cs); err != nil {
		internalErr(w, r, "finder: search decode failed", err)
		return
	}
	var hits []hit
	for _, c := range cs {
		if h, ok := keep(q, resp.Centroid, c); ok {
			hits = append(hits, h)
			resp.TotalSqft += c.TotalSqm * sqftPerSqm
		}
	}
	sortHits(hits, q.sortMode())
	resp.Total = len(hits)
	resp.TotalSqft = math.Round(resp.TotalSqft)

	base := config.GetAppContext(r).Config.AWS.PublicMediaBaseURL
	for _, h := range hits[:min(q.Limit, len(hits))] {
		resp.Results = append(resp.Results, cardOf(h, base))
	}
	if resp.Total == 0 {
		resp.Message = "No warehouses match all of these filters. Try a wider radius, a smaller area or fewer features."
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// cardOf builds the result card; rate and coverUrl as the search cards do.
func cardOf(h hit, mediaBase string) searchCard {
	c := h.C
	card := searchCard{
		ShortID: c.ShortID, Slug: c.Slug, Name: c.Name, City: c.City, Locality: c.Locality, Loc: pointOf(c.Loc),
		TotalSqm: c.TotalSqm, TotalSqft: math.Round(c.TotalSqm * sqftPerSqm),
		CoverURL: domain.PublicMediaURL(mediaBase, c.CoverKey), Type: typeOf(c.Chips),
		DockDoors: numOf(c.Nums, "docks.dock_doors"), HoldCapacityMt: numOf(c.Nums, "warehouse.hold_capacity_mt"),
		Tags: tagsOf(c), NearPort: h.NearPort, NearAirport: h.NearAirport, NearRail: h.NearRail,
	}
	if h.DistKm != nil {
		d := math.Round(*h.DistKm*10) / 10
		card.DistKm = &d
	}
	if c.Live != nil {
		if fv := c.Live.Attributes.Warehouse.Fields.Rent; fv != nil {
			if m, ok := domain.DecodeValue[models.Money](fv.V); ok {
				card.Rate = domain.NewPublicRate(m, c.Price)
			}
		}
		card.ClearHeight = measureOf(c.Live.Attributes.Building.Fields.ClearHeight, domain.UnitM)
	}
	return card
}

// measureOf is a number field as entered (raw value + unit), falling back to
// the canonical value in canonicalUnit; nil when absent.
func measureOf(fv *models.FieldValue, canonicalUnit string) *measure {
	if fv == nil {
		return nil
	}
	if fv.Raw != nil && fv.Raw.Unit != "" {
		if v, ok := toFloat(fv.Raw.Value); ok {
			return &measure{Value: v, Unit: fv.Raw.Unit}
		}
	}
	if v, ok := toFloat(fv.V); ok {
		return &measure{Value: v, Unit: canonicalUnit}
	}
	return nil
}

// toFloat reads a BSON-decoded number of any width.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}
