package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Structured search over the live `warehouses` projection (spec 04). The
// search service validates the filters against the attribute tree and hands
// over a SearchQuery; everything Mongo-shaped lives here.

// EnsureWarehouseSearchIndexes adds the search indexes (spec 04, owned by
// search). Docs without a pin (never published) are skipped by 2dsphere.
func EnsureWarehouseSearchIndexes(ctx context.Context) error {
	if _, err := warehouses().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "loc", Value: "2dsphere"}, {Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "country", Value: 1}}},
		{Keys: bson.D{{Key: "chips", Value: 1}}},
		{Keys: bson.D{{Key: "unk", Value: 1}}},
		{Keys: bson.D{{Key: "fit", Value: 1}}},
		{Keys: bson.D{{Key: "nums.k", Value: 1}, {Key: "nums.v", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "price.per_sqm_month", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure warehouses search indexes: %w", err)
	}
	return nil
}

// SearchQuery is a validated filter set (spec 04 Base match).
type SearchQuery struct {
	// Country, when set, limits to listings in that country.
	Country          string
	AreaMin, AreaMax *float64
	// Price bounds per_sqm_month in PriceCurrency; price-on-request
	// listings always pass a price filter (D-075).
	PriceCurrency      string
	PriceMin, PriceMax *float64
	// Industries must all be F or P (or U with IncludeUnverified).
	Industries        []string
	IncludeUnverified bool
	Chips             []SearchChip
	Ranges            []SearchRange
	// Exclude drops one warehouse (the 410 page's own listing).
	Exclude primitive.ObjectID
}

// SearchChip is one chip filter; Unk lists the `unk` keys that make it
// "unknown" for include-unverified (the chip's node and field path).
type SearchChip struct {
	Chip string
	Unk  []string
}

// SearchRange is one numeric filter: every Cond must hold (a range field
// needs two), or with include-unverified any Unk key is enough.
type SearchRange struct {
	Conds []NumCond
	Unk   []string
}

// NumCond bounds one `nums` fact.
type NumCond struct {
	K        string
	Gte, Lte *float64
}

func rangeOf(gte, lte *float64) bson.M {
	r := bson.M{}
	if gte != nil {
		r["$gte"] = *gte
	}
	if lte != nil {
		r["$lte"] = *lte
	}
	return r
}

// SearchMatch builds the base match (spec 04): live, country, total area,
// price (or on request), chips AND, ranges AND, every industry passing.
func SearchMatch(q SearchQuery) bson.M {
	m := bson.M{"status": WarehouseLive}
	if q.Country != "" {
		m["country"] = q.Country
	}
	if !q.Exclude.IsZero() {
		m["_id"] = bson.M{"$ne": q.Exclude}
	}
	if q.AreaMin != nil || q.AreaMax != nil {
		m["total_sqm"] = rangeOf(q.AreaMin, q.AreaMax)
	}
	and := bson.A{}
	if q.PriceMin != nil || q.PriceMax != nil {
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"price.currency": q.PriceCurrency, "price.per_sqm_month": rangeOf(q.PriceMin, q.PriceMax)},
			bson.M{"price.on_request": true},
		}})
	}
	for _, c := range q.Chips {
		known := bson.M{"chips": c.Chip}
		if q.IncludeUnverified && len(c.Unk) > 0 {
			and = append(and, bson.M{"$or": bson.A{known, bson.M{"unk": bson.M{"$in": c.Unk}}}})
		} else {
			and = append(and, known)
		}
	}
	for _, r := range q.Ranges {
		conds := bson.A{}
		for _, c := range r.Conds {
			conds = append(conds, bson.M{"nums": bson.M{"$elemMatch": bson.M{"k": c.K, "v": rangeOf(c.Gte, c.Lte)}}})
		}
		var known bson.M
		if len(conds) == 1 {
			known = conds[0].(bson.M)
		} else {
			known = bson.M{"$and": conds}
		}
		if q.IncludeUnverified && len(r.Unk) > 0 {
			and = append(and, bson.M{"$or": bson.A{known, bson.M{"unk": bson.M{"$in": r.Unk}}}})
		} else {
			and = append(and, known)
		}
	}
	for _, ind := range q.Industries {
		ok := bson.A{ind + ":F", ind + ":P"}
		if q.IncludeUnverified {
			ok = append(ok, ind+":U")
		}
		and = append(and, bson.M{"fit": bson.M{"$in": ok}})
	}
	if len(and) > 0 {
		m["$and"] = and
	}
	return m
}

// SearchSort picks the result order (D-076).
type SearchSort struct {
	// Mode is relevance | distance | price_asc | price_desc | area_desc.
	Mode string
	// Near: results carry dist_m (a location was given).
	Near bool
	// PriceFilter: price-on-request (and unpriced) listings go after the
	// priced ones (D-075). Price sorts always do this.
	PriceFilter bool
	// Industries are the selected industries (relevance tier).
	Industries []string
}

// SearchSortStages returns the $addFields + $sort stages for s. Helper
// fields (_po, _score) are dropped by the card projection.
func SearchSortStages(s SearchSort) mongo.Pipeline {
	add := bson.D{}
	sort := bson.D{}
	priceSort := s.Mode == "price_asc" || s.Mode == "price_desc"
	if s.PriceFilter || priceSort {
		add = append(add, bson.E{Key: "_po", Value: bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{"$price.per_sqm_month", 0}}, 0, 1}}})
		sort = append(sort, bson.E{Key: "_po", Value: 1})
	}
	switch s.Mode {
	case "distance":
		if s.Near {
			sort = append(sort, bson.E{Key: "dist_m", Value: 1})
		}
	case "price_asc":
		sort = append(sort, bson.E{Key: "price.per_sqm_month", Value: 1})
	case "price_desc":
		sort = append(sort, bson.E{Key: "price.per_sqm_month", Value: -1})
	case "area_desc":
		sort = append(sort, bson.E{Key: "total_sqm", Value: -1})
	default: // relevance
		add = append(add, bson.E{Key: "_score", Value: bson.M{"$add": bson.A{
			bson.M{"$multiply": bson.A{relevanceTier(s.Industries), 10}},
			bson.M{"$ifNull": bson.A{"$completeness", 0}},
		}}})
		sort = append(sort, bson.E{Key: "_score", Value: -1})
		if s.Near {
			sort = append(sort, bson.E{Key: "dist_m", Value: 1})
		}
	}
	sort = append(sort, bson.E{Key: "_id", Value: 1})
	var out mongo.Pipeline
	if len(add) > 0 {
		out = append(out, bson.D{{Key: "$addFields", Value: add}})
	}
	return append(out, bson.D{{Key: "$sort", Value: sort}})
}

// relevanceTier: all selected industries F → 3, else any P → 2, else 1
// (only U left). No industry selected → 3 for everyone.
func relevanceTier(industries []string) any {
	if len(industries) == 0 {
		return 3
	}
	fs, ps := bson.A{}, bson.A{}
	for _, i := range industries {
		fs = append(fs, i+":F")
		ps = append(ps, i+":P")
	}
	fit := bson.M{"$ifNull": bson.A{"$fit", bson.A{}}}
	return bson.M{"$cond": bson.A{
		bson.M{"$setIsSubset": bson.A{fs, fit}}, 3,
		bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{bson.M{"$size": bson.M{"$setIntersection": bson.A{ps, fit}}}, 0}}, 2, 1}},
	}}
}

// SearchHit is one result card's raw data.
type SearchHit struct {
	ID       primitive.ObjectID `bson:"_id"`
	ShortID  string             `bson:"short_id"`
	Slug     string             `bson:"slug"`
	Name     string             `bson:"name"`
	City     string             `bson:"city"`
	Locality string             `bson:"locality"`
	DistM    *float64           `bson:"dist_m"`
	TotalSqm float64            `bson:"total_sqm"`
	Price    *Price             `bson:"price"`
	CoverKey string             `bson:"cover_key"`
	Loc      *GeoPoint          `bson:"loc"`
	Fit      []string           `bson:"fit"`
	Chips    []string           `bson:"chips"`
	Unk      []string           `bson:"unk"`
	Nums     []NumFact          `bson:"nums"`
	// Rent and Area are the live root fields (headline rate, area as typed).
	Rent *FieldValue `bson:"rent"`
	Area *FieldValue `bson:"area"`
}

var hitProjection = bson.M{
	"short_id": 1, "slug": 1, "name": 1, "city": 1, "locality": 1, "dist_m": 1, "total_sqm": 1,
	"price": 1, "cover_key": 1, "loc": 1, "fit": 1, "chips": 1, "unk": 1, "nums": 1,
	"rent": "$live.attributes.warehouse.fields.rent",
	"area": "$live.attributes.warehouse.fields.total_area",
}

// SearchExec is one results query.
type SearchExec struct {
	Match bson.M
	// Near (with MaxMeters) runs $geoNear; nil = no location.
	Near      *GeoPoint
	MaxMeters float64
	Sort      SearchSort
	Skip      int
	Limit     int
}

// SearchResult is the $facet output.
type SearchResult struct {
	Hits  []SearchHit
	Total int64
	// ChipCounts / FitCounts count values within all matches.
	ChipCounts map[string]int64
	FitCounts  map[string]int64
	NumStats   map[string][2]float64
	// Price is over priced matches; Area over all. Nil when empty.
	Price, Area *[2]float64
}

func geoNearStage(near GeoPoint, maxMeters float64, match bson.M) bson.D {
	return bson.D{{Key: "$geoNear", Value: bson.M{
		"near": near, "key": "loc", "distanceField": "dist_m",
		"maxDistance": maxMeters, "query": match, "spherical": true,
	}}}
}

// RunSearch runs the results query: ($geoNear | $match) → $facet of the
// page, the total and the facet counts (spec 04 Pipeline step 2).
func RunSearch(ctx context.Context, e SearchExec) (SearchResult, error) {
	var p mongo.Pipeline
	if e.Near != nil {
		p = append(p, geoNearStage(*e.Near, e.MaxMeters, e.Match))
	} else {
		p = append(p, bson.D{{Key: "$match", Value: e.Match}})
	}
	results := append(SearchSortStages(e.Sort),
		bson.D{{Key: "$skip", Value: e.Skip}},
		bson.D{{Key: "$limit", Value: e.Limit}},
		bson.D{{Key: "$project", Value: hitProjection}},
	)
	minMax := func(field string) bson.M {
		return bson.M{"_id": nil, "min": bson.M{"$min": field}, "max": bson.M{"$max": field}}
	}
	p = append(p, bson.D{{Key: "$facet", Value: bson.M{
		"results": results,
		"total":   bson.A{bson.M{"$count": "n"}},
		"chips":   bson.A{bson.M{"$unwind": "$chips"}, bson.M{"$group": bson.M{"_id": "$chips", "n": bson.M{"$sum": 1}}}},
		"fit":     bson.A{bson.M{"$unwind": "$fit"}, bson.M{"$group": bson.M{"_id": "$fit", "n": bson.M{"$sum": 1}}}},
		"nums": bson.A{bson.M{"$unwind": "$nums"}, bson.M{"$group": bson.M{"_id": "$nums.k",
			"min": bson.M{"$min": "$nums.v"}, "max": bson.M{"$max": "$nums.v"}}}},
		"price": bson.A{bson.M{"$match": bson.M{"price.per_sqm_month": bson.M{"$gt": 0}}}, bson.M{"$group": minMax("$price.per_sqm_month")}},
		"area":  bson.A{bson.M{"$match": bson.M{"total_sqm": bson.M{"$gt": 0}}}, bson.M{"$group": minMax("$total_sqm")}},
	}}})

	cur, err := warehouses().Aggregate(ctx, p)
	if err != nil {
		return SearchResult{}, err
	}
	defer cur.Close(ctx)
	type kv struct {
		ID  string  `bson:"_id"`
		N   int64   `bson:"n"`
		Min float64 `bson:"min"`
		Max float64 `bson:"max"`
	}
	var rows []struct {
		Results []SearchHit `bson:"results"`
		Total   []kv        `bson:"total"`
		Chips   []kv        `bson:"chips"`
		Fit     []kv        `bson:"fit"`
		Nums    []kv        `bson:"nums"`
		Price   []kv        `bson:"price"`
		Area    []kv        `bson:"area"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Hits: []SearchHit{}, ChipCounts: map[string]int64{}, FitCounts: map[string]int64{}, NumStats: map[string][2]float64{}}
	if len(rows) == 0 {
		return out, nil
	}
	r := rows[0]
	out.Hits = append(out.Hits, r.Results...)
	if len(r.Total) > 0 {
		out.Total = r.Total[0].N
	}
	for _, c := range r.Chips {
		out.ChipCounts[c.ID] = c.N
	}
	for _, f := range r.Fit {
		out.FitCounts[f.ID] = f.N
	}
	for _, n := range r.Nums {
		out.NumStats[n.ID] = [2]float64{n.Min, n.Max}
	}
	if len(r.Price) > 0 {
		out.Price = &[2]float64{r.Price[0].Min, r.Price[0].Max}
	}
	if len(r.Area) > 0 {
		out.Area = &[2]float64{r.Area[0].Min, r.Area[0].Max}
	}
	return out, nil
}

// SearchRingCounts counts matches within each ring (metres, ascending),
// cumulatively (spec 04 Pipeline step 1).
func SearchRingCounts(ctx context.Context, near GeoPoint, match bson.M, ringsM []float64) ([]int64, error) {
	if len(ringsM) == 0 {
		return nil, nil
	}
	last := ringsM[len(ringsM)-1]
	bounds := bson.A{0.0}
	for i, r := range ringsM {
		if i == len(ringsM)-1 {
			r++ // $bucket's upper bound is exclusive
		}
		bounds = append(bounds, r)
	}
	cur, err := warehouses().Aggregate(ctx, mongo.Pipeline{
		geoNearStage(near, last, match),
		{{Key: "$bucket", Value: bson.M{"groupBy": "$dist_m", "boundaries": bounds, "default": "beyond",
			"output": bson.M{"n": bson.M{"$sum": 1}}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	per := make([]int64, len(ringsM))
	for cur.Next(ctx) {
		var row struct {
			ID any   `bson:"_id"`
			N  int64 `bson:"n"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		lower, ok := row.ID.(float64)
		if !ok {
			continue // "beyond"
		}
		for i := range ringsM {
			if lower == bounds[i].(float64) {
				per[i] += row.N
			}
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	for i := 1; i < len(per); i++ {
		per[i] += per[i-1]
	}
	return per, nil
}

// MapPoint is one map marker.
type MapPoint struct {
	ShortID string   `bson:"short_id"`
	Loc     GeoPoint `bson:"loc"`
	Price   *Price   `bson:"price"`
}

// SearchMapPoints returns every match's pin (unpaginated, D-077); with near,
// only those within maxMeters.
func SearchMapPoints(ctx context.Context, match bson.M, near *GeoPoint, maxMeters float64) ([]MapPoint, error) {
	proj := bson.M{"short_id": 1, "loc": 1, "price": 1}
	if near != nil {
		cur, err := warehouses().Aggregate(ctx, mongo.Pipeline{
			geoNearStage(*near, maxMeters, match),
			{{Key: "$project", Value: proj}},
		})
		if err != nil {
			return nil, err
		}
		out := []MapPoint{}
		err = cur.All(ctx, &out)
		return out, err
	}
	f := bson.M{"loc": bson.M{"$exists": true}}
	for k, v := range match {
		f[k] = v
	}
	return findAllDocs[MapPoint](ctx, warehouses(), f, options.Find().SetProjection(proj))
}

// NearestLiveWarehouses returns the limit nearest live listings to near,
// except exclude (03's 410 page).
func NearestLiveWarehouses(ctx context.Context, near GeoPoint, limit int, exclude primitive.ObjectID) ([]SearchHit, error) {
	match := SearchMatch(SearchQuery{Exclude: exclude})
	cur, err := warehouses().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$geoNear", Value: bson.M{"near": near, "key": "loc", "distanceField": "dist_m", "query": match, "spherical": true}}},
		{{Key: "$limit", Value: limit}},
		{{Key: "$project", Value: hitProjection}},
	})
	if err != nil {
		return nil, err
	}
	out := []SearchHit{}
	err = cur.All(ctx, &out)
	return out, err
}
