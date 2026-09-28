package metrics

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	dfsdr "github.com/atharva-ng/crunch/internal/analytics/sources/dfsdomainrating"
	gscsrc "github.com/atharva-ng/crunch/internal/analytics/sources/gsc"
	"github.com/atharva-ng/crunch/internal/models"
)

// The v1 definitions. The requirements' "roi_split" is decomposed into the
// article_* twins of the site metrics — the uniform Result envelope carries
// one series per metric, and the ROI card composes clicks_trend +
// article_clicks_trend (and the two totals) client-side.

var clicksTrend = Def{
	Name: "clicks_trend", Sources: []string{gscsrc.SourceName}, Kind: KindSeries,
	Build: siteTrendBuilder(gscsrc.MetClicks),
}

var impressionsTrend = Def{
	Name: "impressions_trend", Sources: []string{gscsrc.SourceName}, Kind: KindSeries,
	Build: siteTrendBuilder(gscsrc.MetImpressions),
}

// Per-day ctr/position come straight off the site-grain row (one row per day —
// the recompute-from-sums rule applies to aggregation ACROSS rows, not here).
var ctrTrend = Def{
	Name: "ctr_trend", Sources: []string{gscsrc.SourceName}, Kind: KindSeries,
	Build: siteTrendBuilder(gscsrc.MetCTR),
}

var positionTrend = Def{
	Name: "position_trend", Sources: []string{gscsrc.SourceName}, Kind: KindSeries,
	Build: siteTrendBuilder(gscsrc.MetPosition),
}

var articleClicksTrend = Def{
	Name: "article_clicks_trend", Sources: []string{gscsrc.SourceName}, Kind: KindSeries,
	Build: func(ctx context.Context, q Query) (Result, error) {
		points, err := articleClicksDaily(ctx, q.WebEntityID, q.From, q.To)
		if err != nil {
			return Result{}, err
		}
		return Result{Points: points}, nil
	},
}

var domainRatingTrend = Def{
	Name: "domain_rating_trend", Sources: []string{dfsdr.SourceName}, Kind: KindSeries,
	Build: func(ctx context.Context, q Query) (Result, error) {
		var rows []struct {
			Date    string             `bson:"date"`
			Metrics map[string]float64 `bson:"metrics"`
		}
		err := models.AggregateAnalyticsFacts(ctx, []bson.M{
			{"$match": factMatch(q.WebEntityID, dfsdr.SourceName, dfsdr.GrainDomainRating, q.From, q.To)},
			{"$sort": bson.M{"date": 1}},
		}, &rows)
		if err != nil {
			return Result{}, err
		}
		points := make([]SeriesPoint, 0, len(rows))
		for _, r := range rows {
			points = append(points, SeriesPoint{Date: r.Date, Value: r.Metrics[dfsdr.MetRating]})
		}
		return Result{Points: points}, nil
	},
}

var clicksTotal = Def{
	Name: "clicks_total", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: siteKPIBuilder(func(t siteTotalsRow) float64 { return t.Clicks }),
}

var impressionsTotal = Def{
	Name: "impressions_total", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: siteKPIBuilder(func(t siteTotalsRow) float64 { return t.Impressions }),
}

// Aggregated CTR/position are RECOMPUTED from sums (requirements Step 4 rule):
// ctr = Σclicks/Σimpressions, position = impression-weighted mean.
var ctrAvg = Def{
	Name: "ctr_avg", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: siteKPIBuilder(func(t siteTotalsRow) float64 {
		if t.Impressions == 0 {
			return 0
		}
		return t.Clicks / t.Impressions
	}),
}

var positionAvg = Def{
	Name: "position_avg", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: siteKPIBuilder(func(t siteTotalsRow) float64 {
		if t.Impressions == 0 {
			return 0
		}
		return t.PositionWeighted / t.Impressions
	}),
}

var articleClicksTotal = Def{
	Name: "article_clicks_total", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: func(ctx context.Context, q Query) (Result, error) {
		value, err := articleClicksSum(ctx, q.WebEntityID, q.From, q.To)
		if err != nil {
			return Result{}, err
		}
		result := Result{Value: &value}
		if q.PrevFrom != "" {
			prev, err := articleClicksSum(ctx, q.WebEntityID, q.PrevFrom, q.PrevTo)
			if err != nil {
				return Result{}, err
			}
			result.PrevValue = &prev
		}
		return result, nil
	},
}

// traffic_value: Σ(per-query clicks × stored keyword CPC) over the range —
// "worth $X in ad spend", matched on lowercase-trimmed query text against the
// entity's stored keywords (plan D13; only matched queries carry a CPC).
var trafficValue = Def{
	Name: "traffic_value", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: trafficValueBuilder(false),
}

// article_traffic_value: same join restricted to article-stamped page_query
// rows — the "our generated articles" split of the ROI card.
var articleTrafficValue = Def{
	Name: "article_traffic_value", Sources: []string{gscsrc.SourceName}, Kind: KindKPI,
	Build: trafficValueBuilder(true),
}

// ---- shared builders ----

func factMatch(entityID primitive.ObjectID, source, grain, from, to string) bson.M {
	match := bson.M{
		"web_entity_id": entityID,
		"source":        source,
		"grain":         grain,
	}
	dateCond := bson.M{"$lte": to}
	if from != "" {
		dateCond["$gte"] = from
	}
	match["date"] = dateCond
	return match
}

func siteTrendBuilder(metricKey string) func(ctx context.Context, q Query) (Result, error) {
	return func(ctx context.Context, q Query) (Result, error) {
		var rows []struct {
			Date    string             `bson:"date"`
			Metrics map[string]float64 `bson:"metrics"`
		}
		err := models.AggregateAnalyticsFacts(ctx, []bson.M{
			{"$match": factMatch(q.WebEntityID, gscsrc.SourceName, gscsrc.GrainSite, q.From, q.To)},
			{"$sort": bson.M{"date": 1}},
		}, &rows)
		if err != nil {
			return Result{}, err
		}
		points := make([]SeriesPoint, 0, len(rows))
		for _, r := range rows {
			points = append(points, SeriesPoint{Date: r.Date, Value: r.Metrics[metricKey]})
		}
		return Result{Points: points}, nil
	}
}

type siteTotalsRow struct {
	Clicks           float64 `bson:"clicks"`
	Impressions      float64 `bson:"impressions"`
	PositionWeighted float64 `bson:"positionWeighted"`
}

func siteTotals(ctx context.Context, entityID primitive.ObjectID, from, to string) (siteTotalsRow, error) {
	var rows []siteTotalsRow
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": factMatch(entityID, gscsrc.SourceName, gscsrc.GrainSite, from, to)},
		{"$group": bson.M{
			"_id":         nil,
			"clicks":      bson.M{"$sum": "$metrics." + gscsrc.MetClicks},
			"impressions": bson.M{"$sum": "$metrics." + gscsrc.MetImpressions},
			"positionWeighted": bson.M{"$sum": bson.M{"$multiply": bson.A{
				"$metrics." + gscsrc.MetPosition, "$metrics." + gscsrc.MetImpressions,
			}}},
		}},
	}, &rows)
	if err != nil {
		return siteTotalsRow{}, err
	}
	if len(rows) == 0 {
		return siteTotalsRow{}, nil
	}
	return rows[0], nil
}

func siteKPIBuilder(extract func(siteTotalsRow) float64) func(ctx context.Context, q Query) (Result, error) {
	return func(ctx context.Context, q Query) (Result, error) {
		totals, err := siteTotals(ctx, q.WebEntityID, q.From, q.To)
		if err != nil {
			return Result{}, err
		}
		value := extract(totals)
		result := Result{Value: &value}
		if q.PrevFrom != "" {
			prevTotals, err := siteTotals(ctx, q.WebEntityID, q.PrevFrom, q.PrevTo)
			if err != nil {
				return Result{}, err
			}
			prev := extract(prevTotals)
			result.PrevValue = &prev
		}
		return result, nil
	}
}

func articleMatch(entityID primitive.ObjectID, from, to string) bson.M {
	match := factMatch(entityID, gscsrc.SourceName, gscsrc.GrainPage, from, to)
	match["dims."+gscsrc.DimArticleID] = bson.M{"$exists": true}
	return match
}

func articleClicksDaily(ctx context.Context, entityID primitive.ObjectID, from, to string) ([]SeriesPoint, error) {
	var rows []struct {
		Date   string  `bson:"_id"`
		Clicks float64 `bson:"clicks"`
	}
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": articleMatch(entityID, from, to)},
		{"$group": bson.M{"_id": "$date", "clicks": bson.M{"$sum": "$metrics." + gscsrc.MetClicks}}},
		{"$sort": bson.M{"_id": 1}},
	}, &rows)
	if err != nil {
		return nil, err
	}
	points := make([]SeriesPoint, 0, len(rows))
	for _, r := range rows {
		points = append(points, SeriesPoint{Date: r.Date, Value: r.Clicks})
	}
	return points, nil
}

func articleClicksSum(ctx context.Context, entityID primitive.ObjectID, from, to string) (float64, error) {
	var rows []struct {
		Clicks float64 `bson:"clicks"`
	}
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": articleMatch(entityID, from, to)},
		{"$group": bson.M{"_id": nil, "clicks": bson.M{"$sum": "$metrics." + gscsrc.MetClicks}}},
	}, &rows)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return rows[0].Clicks, nil
}

// queryClicks sums clicks per query text over the range — query grain for the
// whole site, article-stamped page_query grain for the articles split.
func queryClicks(ctx context.Context, entityID primitive.ObjectID, from, to string, articleOnly bool) (map[string]float64, error) {
	grain := gscsrc.GrainQuery
	match := factMatch(entityID, gscsrc.SourceName, grain, from, to)
	if articleOnly {
		match = factMatch(entityID, gscsrc.SourceName, gscsrc.GrainPageQuery, from, to)
		match["dims."+gscsrc.DimArticleID] = bson.M{"$exists": true}
	}
	var rows []struct {
		Query  string  `bson:"_id"`
		Clicks float64 `bson:"clicks"`
	}
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": match},
		{"$group": bson.M{"_id": "$dims." + gscsrc.DimQuery, "clicks": bson.M{"$sum": "$metrics." + gscsrc.MetClicks}}},
	}, &rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.Query] = r.Clicks
	}
	return out, nil
}

func trafficValueFor(ctx context.Context, q Query, from, to string, articleOnly bool, stats map[string]models.KeywordStats) (float64, error) {
	clicks, err := queryClicks(ctx, q.WebEntityID, from, to, articleOnly)
	if err != nil {
		return 0, err
	}
	var total float64
	for query, c := range clicks {
		if kw, ok := stats[strings.ToLower(strings.TrimSpace(query))]; ok && kw.CPC > 0 {
			total += c * kw.CPC
		}
	}
	return total, nil
}

func trafficValueBuilder(articleOnly bool) func(ctx context.Context, q Query) (Result, error) {
	return func(ctx context.Context, q Query) (Result, error) {
		stats, err := models.FindKeywordStatsByWebEntityContexts(ctx, q.WECIDs)
		if err != nil {
			return Result{}, err
		}
		value, err := trafficValueFor(ctx, q, q.From, q.To, articleOnly, stats)
		if err != nil {
			return Result{}, err
		}
		result := Result{Value: &value}
		if q.PrevFrom != "" {
			prev, err := trafficValueFor(ctx, q, q.PrevFrom, q.PrevTo, articleOnly, stats)
			if err != nil {
				return Result{}, err
			}
			result.PrevValue = &prev
		}
		return result, nil
	}
}
