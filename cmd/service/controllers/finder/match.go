package finder

import (
	"cmp"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// POST /v1/public/finder/match: the 5-question wizard (top 3, scored by
// scoring.go). Lives beside the filter search (handlers.go / search.go).

// topN is how many results the wizard returns.
const topN = 3

// matchRequest is the POST /v1/public/finder/match body.
type matchRequest struct {
	City       string   `json:"city"`
	Type       string   `json:"type"`
	AreaSqft   float64  `json:"areaSqft"`
	Goods      []string `json:"goods"`
	Industries []string `json:"industries"`
	NearPort   bool     `json:"nearPort"`
	NearRail   bool     `json:"nearRail"`

	// rawCity is city as sent, before the shared sanitizer (set by the
	// handler from captureRawText; see parsePlace).
	rawCity string
}

// matchCard is one result, with the search card's field names and shapes.
type matchCard struct {
	ShortID      string             `json:"shortId"`
	Slug         string             `json:"slug"`
	Name         string             `json:"name"`
	City         string             `json:"city,omitempty"`
	Locality     string             `json:"locality,omitempty"`
	TotalSqm     float64            `json:"totalSqm"`
	Rate         *domain.PublicRate `json:"rate,omitempty"`
	CoverURL     string             `json:"coverUrl,omitempty"`
	MatchPercent int                `json:"matchPercent"`
	Matched      []string           `json:"matched"`
	TypeUnknown  bool               `json:"typeUnknown"`
	Reason       string             `json:"reason"`
}

type matchResponse struct {
	Results []matchCard `json:"results"`
	Message string      `json:"message,omitempty"`
}

// HandleMatch returns the top 3 live listings for the answers.
func HandleMatch(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(matchRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	req.rawCity = rawTextOf(r).City
	q, msg := parseCriteria(req)
	if msg != "" {
		badRequest(w, r, msg)
		return
	}
	if q.CityGone {
		middleware.SendJSONResponse(w, r, http.StatusOK, matchResponse{Results: []matchCard{}, Message: msgPlaceCleanedAway})
		return
	}

	cur, err := models.Collection(collWarehouses).Find(r.Context(), matchFilter(q), options.Find().SetProjection(bson.M{
		"short_id": 1, "slug": 1, "name": 1, "city": 1, "locality": 1, "loc": 1, "total_sqm": 1, "price": 1,
		"cover_key": 1, "chips": 1, "fit": 1, "completeness": 1, "verified_ratio": 1,
		"live.attributes.warehouse.fields.rent": 1,
	}))
	if err != nil {
		queryErr(w, r, "finder: match query failed", err)
		return
	}
	var cs []matchCandidate
	if err := cur.All(r.Context(), &cs); err != nil {
		internalErr(w, r, "finder: match decode failed", err)
		return
	}

	base := config.GetAppContext(r).Config.AWS.PublicMediaBaseURL
	resp := matchResponse{Results: []matchCard{}}
	for _, s := range rank(q, cs, topN) {
		resp.Results = append(resp.Results, matchCardOf(s, base))
	}
	if len(resp.Results) == 0 {
		resp.Message = "No live warehouses in " + clip(q.City) + " match that size and storage yet. " +
			"Try a smaller area, fewer storage needs or another type."
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// parseCriteria validates req; a non-empty message is a 400.
func parseCriteria(req matchRequest) (criteria, string) {
	q := criteria{Type: strings.ToLower(strings.TrimSpace(req.Type)), NearPort: req.NearPort, NearRail: req.NearRail}
	var msg string
	if q.City, q.CityGone, msg = parsePlace("city", req.City, req.rawCity); msg != "" {
		return q, msg
	}
	if q.City == "" && !q.CityGone {
		return q, "city is required"
	}
	if msg := cmp.Or(checkNumber("areaSqft", req.AreaSqft, maxAreaSqft),
		checkList("goods", len(req.Goods), maxGoods),
		checkList("industries", len(req.Industries), maxIndustries)); msg != "" {
		return q, msg
	}
	if q.Type != "" && typeLabels[q.Type] == "" {
		return q, "type must be general, bonded, ftwz or empty"
	}
	if req.AreaSqft <= 0 {
		return q, "areaSqft must be above 0"
	}
	q.NeededSqm = sqftToSqm(req.AreaSqft)
	for _, g := range req.Goods {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case goodsDG:
			q.DG = true
		case goodsCold:
			q.Cold = true
		case goodsGeneral:
		default:
			return q, "goods may only contain general, dg and cold"
		}
	}
	for _, ind := range req.Industries {
		ind = strings.ToLower(strings.TrimSpace(ind))
		if industryLabels[ind] != "" && !slices.Contains(q.Industries, ind) {
			q.Industries = append(q.Industries, ind)
		}
	}
	return q, ""
}

// matchFilter is the hard filter: live, the city (case-insensitive exact),
// at least 80% of the asked area, and the storage the goods need. Type,
// industries, port and rail are applied in Go (rank / score).
func matchFilter(q criteria) bson.M {
	f := bson.M{
		"status":    models.WarehouseLive,
		"city":      bson.M{"$regex": "^" + regexp.QuoteMeta(q.City) + "$", "$options": "i"},
		"total_sqm": bson.M{"$gte": q.minSqm()},
	}
	var chips []string
	if q.DG {
		chips = append(chips, "hazmat")
	}
	if q.Cold {
		chips = append(chips, "cold_storage")
	}
	if len(chips) > 0 {
		f["chips"] = bson.M{"$all": chips}
	}
	return f
}

// matchCardOf builds the result card; rate and coverUrl as the search cards
// do.
func matchCardOf(s scored, mediaBase string) matchCard {
	c := s.C
	card := matchCard{
		ShortID: c.ShortID, Slug: c.Slug, Name: c.Name, City: c.City, Locality: c.Locality, TotalSqm: c.TotalSqm,
		CoverURL: domain.PublicMediaURL(mediaBase, c.CoverKey), MatchPercent: s.Percent(),
		Matched: []string{}, TypeUnknown: s.TypeUnknown, Reason: reason(s.Matched),
	}
	if fv := c.rent(); fv != nil {
		if m, ok := domain.DecodeValue[models.Money](fv.V); ok {
			card.Rate = domain.NewPublicRate(m, c.Price)
		}
	}
	for _, m := range s.Matched {
		card.Matched = append(card.Matched, m.Label)
	}
	return card
}
