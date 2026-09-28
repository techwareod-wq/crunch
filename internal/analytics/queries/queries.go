// Package queries holds the analytics read layer's bespoke table builders
// (LLD §5.2): the pages, articles and queries tables join facts against
// scheduledArticle/keyword docs — shapes the generic metric envelope can't
// express. All aggregation follows the recompute rule: table CTR/position are
// derived from sums (Σclicks/Σimpressions, impression-weighted position),
// never averaged from row values.
package queries

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics"
	gscsrc "github.com/atharva-ng/crunch/internal/analytics/sources/gsc"
	"github.com/atharva-ng/crunch/internal/models"
)

// Read-time tunables (requirements Step 7: constants, tunable without
// migration).
const (
	// decayThreshold flags a page when its last-28-day clicks fall below this
	// fraction of the prior 28 days'.
	decayThreshold = 0.5
	// decayMinPriorClicks keeps low-traffic and freshly published pages from
	// all flagging as decayed.
	decayMinPriorClicks = 10
	// strikingMinImpressions is the impressions floor of the
	// striking-distance filter (position 5–20 AND impressions above this).
	strikingMinImpressions = 100
	// strikingPositionMin/Max bound the striking-distance position band.
	strikingPositionMin = 5.0
	strikingPositionMax = 20.0
	// tableRowCap bounds every table response.
	tableRowCap = 200
	// topQueriesPerArticle bounds the article-detail query list.
	topQueriesPerArticle = 25
	// demandWindowDays is the fixed window demand capture compares against
	// keyword.Volume (a monthly search count) — independent of the selected
	// range so the percentage stays meaningful.
	demandWindowDays = 28
)

// aggRow is the shared group-stage output shape.
type aggRow struct {
	ID          string  `bson:"_id"`
	Clicks      float64 `bson:"clicks"`
	Impressions float64 `bson:"impressions"`
	PosWeighted float64 `bson:"posWeighted"`
	IsArticle   bool    `bson:"isArticle"`
	ArticleID   string  `bson:"articleId"`
}

func derivedCTR(clicks, impressions float64) float64 {
	if impressions == 0 {
		return 0
	}
	return clicks / impressions
}

func derivedPosition(posWeighted, impressions float64) float64 {
	if impressions == 0 {
		return 0
	}
	return posWeighted / impressions
}

func factMatch(entityID primitive.ObjectID, grain, from, to string) bson.M {
	match := bson.M{
		"web_entity_id": entityID,
		"source":        gscsrc.SourceName,
		"grain":         grain,
	}
	dateCond := bson.M{"$lte": to}
	if from != "" {
		dateCond["$gte"] = from
	}
	match["date"] = dateCond
	return match
}

func sumStage(idExpr any) bson.M {
	return bson.M{
		"_id":         idExpr,
		"clicks":      bson.M{"$sum": "$metrics." + gscsrc.MetClicks},
		"impressions": bson.M{"$sum": "$metrics." + gscsrc.MetImpressions},
		"posWeighted": bson.M{"$sum": bson.M{"$multiply": bson.A{
			"$metrics." + gscsrc.MetPosition, "$metrics." + gscsrc.MetImpressions,
		}}},
	}
}

// demandWindow is the [settled−27 … settled] window demand capture reads.
func demandWindow(settled string) (string, string) {
	d, err := time.Parse(analytics.DateLayout, settled)
	if err != nil {
		return settled, settled
	}
	return d.AddDate(0, 0, -(demandWindowDays - 1)).Format(analytics.DateLayout), settled
}

// ---- pages table ----

// PageRow is one row of the pages table.
type PageRow struct {
	Page        string  `json:"page"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
	IsArticle   bool    `json:"isArticle"`
	ArticleID   string  `json:"articleId,omitempty"`
	// Decayed: last-28-day clicks < decayThreshold × the prior 28 days'
	// (with the decayMinPriorClicks floor). Always computed over the fixed
	// window ending at the latest settled date, independent of the range.
	Decayed bool `json:"decayed"`
}

// PagesTable builds the per-page rollup over [from, to], top tableRowCap pages
// by clicks.
func PagesTable(ctx context.Context, entityID primitive.ObjectID, from, to, settled string) ([]PageRow, error) {
	group := sumStage("$dims." + gscsrc.DimPage)
	group["isArticle"] = bson.M{"$max": bson.M{"$cond": bson.A{
		bson.M{"$gt": bson.A{"$dims." + gscsrc.DimArticleID, nil}}, true, false,
	}}}
	group["articleId"] = bson.M{"$max": "$dims." + gscsrc.DimArticleID}

	var rows []aggRow
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": factMatch(entityID, gscsrc.GrainPage, from, to)},
		{"$group": group},
		{"$sort": bson.M{"clicks": -1, "_id": 1}},
		{"$limit": tableRowCap},
	}, &rows)
	if err != nil {
		return nil, err
	}

	decayed, err := decayedPages(ctx, entityID, settled)
	if err != nil {
		return nil, err
	}

	out := make([]PageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, PageRow{
			Page:        r.ID,
			Clicks:      r.Clicks,
			Impressions: r.Impressions,
			CTR:         derivedCTR(r.Clicks, r.Impressions),
			Position:    derivedPosition(r.PosWeighted, r.Impressions),
			IsArticle:   r.IsArticle,
			ArticleID:   r.ArticleID,
			Decayed:     decayed[r.ID],
		})
	}
	return out, nil
}

// decayedPages computes the decay flag per page over the fixed 56-day window
// ending at the latest settled date.
func decayedPages(ctx context.Context, entityID primitive.ObjectID, settled string) (map[string]bool, error) {
	end, err := time.Parse(analytics.DateLayout, settled)
	if err != nil {
		return map[string]bool{}, nil
	}
	last28Start := end.AddDate(0, 0, -27).Format(analytics.DateLayout)
	prior28Start := end.AddDate(0, 0, -55).Format(analytics.DateLayout)

	var rows []struct {
		ID    string  `bson:"_id"`
		Last  float64 `bson:"last"`
		Prior float64 `bson:"prior"`
	}
	err = models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": factMatch(entityID, gscsrc.GrainPage, prior28Start, settled)},
		{"$group": bson.M{
			"_id": "$dims." + gscsrc.DimPage,
			"last": bson.M{"$sum": bson.M{"$cond": bson.A{
				bson.M{"$gte": bson.A{"$date", last28Start}},
				"$metrics." + gscsrc.MetClicks, 0,
			}}},
			"prior": bson.M{"$sum": bson.M{"$cond": bson.A{
				bson.M{"$lt": bson.A{"$date", last28Start}},
				"$metrics." + gscsrc.MetClicks, 0,
			}}},
		}},
	}, &rows)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		if r.Prior >= decayMinPriorClicks && r.Last < decayThreshold*r.Prior {
			out[r.ID] = true
		}
	}
	return out, nil
}

// ---- articles table ----

// ArticleRow is one row of the per-article rollup.
type ArticleRow struct {
	ArticleID   string  `json:"articleId"`
	Title       string  `json:"title,omitempty"`
	PublishedAt string  `json:"publishedAt,omitempty"` // schedule date, "2006-01-02"
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
	// TrafficValue is Σ(query clicks × stored keyword CPC) over this article's
	// page_query rows — "based on your tracked keywords".
	TrafficValue float64 `json:"trafficValue"`
	// Target keyword tracking (the keyword the article was generated from).
	TargetKeyword         string  `json:"targetKeyword,omitempty"`
	TargetKeywordVolume   int     `json:"targetKeywordVolume,omitempty"`
	TargetKeywordClicks   float64 `json:"targetKeywordClicks"`
	TargetKeywordPosition float64 `json:"targetKeywordPosition"`
	// DemandCapture is target-keyword clicks over the fixed 28-day demand
	// window ÷ keyword.Volume; nil when the keyword has no volume data.
	DemandCapture *float64 `json:"demandCapture,omitempty"`
}

// ArticlesTable builds the per-article rollup over [from, to].
func ArticlesTable(ctx context.Context, entityID primitive.ObjectID, wecIDs []primitive.ObjectID, from, to, settled string) ([]ArticleRow, error) {
	var rows []aggRow
	match := factMatch(entityID, gscsrc.GrainPage, from, to)
	match["dims."+gscsrc.DimArticleID] = bson.M{"$exists": true}
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": match},
		{"$group": sumStage("$dims." + gscsrc.DimArticleID)},
		{"$sort": bson.M{"clicks": -1, "_id": 1}},
		{"$limit": tableRowCap},
	}, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []ArticleRow{}, nil
	}

	articleIDs := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		if id, err := primitive.ObjectIDFromHex(r.ID); err == nil {
			articleIDs = append(articleIDs, id)
		}
	}
	articles, err := models.FindScheduledArticlesByIDs(ctx, articleIDs)
	if err != nil {
		return nil, err
	}
	keywordIDs := make([]primitive.ObjectID, 0, len(articles))
	for _, a := range articles {
		if !a.KeywordID.IsZero() {
			keywordIDs = append(keywordIDs, a.KeywordID)
		}
	}
	keywords, err := models.FindKeywordsByIDs(ctx, keywordIDs)
	if err != nil {
		return nil, err
	}
	stats, err := models.FindKeywordStatsByWebEntityContexts(ctx, wecIDs)
	if err != nil {
		return nil, err
	}

	// Per-(article, query) sums over the range: traffic value + target
	// keyword performance in one pass.
	rangeAQ, err := articleQuerySums(ctx, entityID, from, to)
	if err != nil {
		return nil, err
	}
	dFrom, dTo := demandWindow(settled)
	demandAQ, err := articleQuerySums(ctx, entityID, dFrom, dTo)
	if err != nil {
		return nil, err
	}

	out := make([]ArticleRow, 0, len(rows))
	for _, r := range rows {
		row := ArticleRow{
			ArticleID:   r.ID,
			Clicks:      r.Clicks,
			Impressions: r.Impressions,
			CTR:         derivedCTR(r.Clicks, r.Impressions),
			Position:    derivedPosition(r.PosWeighted, r.Impressions),
		}
		var targetText string
		if id, idErr := primitive.ObjectIDFromHex(r.ID); idErr == nil {
			if article := articles[id]; article != nil {
				row.Title = article.Title
				if !article.ScheduleDate.IsZero() {
					row.PublishedAt = article.ScheduleDate.Format(analytics.DateLayout)
				}
				if kw := keywords[article.KeywordID]; kw != nil {
					row.TargetKeyword = kw.Keyword
					row.TargetKeywordVolume = kw.Volume
					targetText = strings.ToLower(strings.TrimSpace(kw.Keyword))
				}
			}
		}
		for _, aq := range rangeAQ[r.ID] {
			if kw, ok := stats[strings.ToLower(strings.TrimSpace(aq.Query))]; ok && kw.CPC > 0 {
				row.TrafficValue += aq.Clicks * kw.CPC
			}
			if targetText != "" && strings.ToLower(strings.TrimSpace(aq.Query)) == targetText {
				row.TargetKeywordClicks = aq.Clicks
				row.TargetKeywordPosition = derivedPosition(aq.PosWeighted, aq.Impressions)
			}
		}
		if targetText != "" && row.TargetKeywordVolume > 0 {
			var demandClicks float64
			for _, aq := range demandAQ[r.ID] {
				if strings.ToLower(strings.TrimSpace(aq.Query)) == targetText {
					demandClicks = aq.Clicks
					break
				}
			}
			capture := demandClicks / float64(row.TargetKeywordVolume)
			row.DemandCapture = &capture
		}
		out = append(out, row)
	}
	return out, nil
}

// articleQuery is one (article, query) rollup.
type articleQuery struct {
	Query       string
	Clicks      float64
	Impressions float64
	PosWeighted float64
}

// articleQuerySums groups article-stamped page_query facts by (article,
// query) over [from, to], keyed by article hex id.
func articleQuerySums(ctx context.Context, entityID primitive.ObjectID, from, to string) (map[string][]articleQuery, error) {
	match := factMatch(entityID, gscsrc.GrainPageQuery, from, to)
	match["dims."+gscsrc.DimArticleID] = bson.M{"$exists": true}
	var rows []struct {
		ID struct {
			Article string `bson:"article"`
			Query   string `bson:"query"`
		} `bson:"_id"`
		Clicks      float64 `bson:"clicks"`
		Impressions float64 `bson:"impressions"`
		PosWeighted float64 `bson:"posWeighted"`
	}
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": match},
		{"$group": sumStage(bson.M{
			"article": "$dims." + gscsrc.DimArticleID,
			"query":   "$dims." + gscsrc.DimQuery,
		})},
	}, &rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]articleQuery{}
	for _, r := range rows {
		out[r.ID.Article] = append(out[r.ID.Article], articleQuery{
			Query:       r.ID.Query,
			Clicks:      r.Clicks,
			Impressions: r.Impressions,
			PosWeighted: r.PosWeighted,
		})
	}
	return out, nil
}

// ---- article detail ----

// ArticleTrendPoint is one day of one article's performance.
type ArticleTrendPoint struct {
	Date        string  `json:"date"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
}

// ArticleQueryRow is one query of the article-detail top-queries list.
type ArticleQueryRow struct {
	Query       string  `json:"query"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// TargetKeywordPoint is one day of the target keyword's position line.
type TargetKeywordPoint struct {
	Date     string  `json:"date"`
	Position float64 `json:"position"`
	Clicks   float64 `json:"clicks"`
}

// ArticleDetail is the one-article analytics view.
type ArticleDetail struct {
	ArticleRow
	Trend              []ArticleTrendPoint  `json:"trend"`
	TopQueries         []ArticleQueryRow    `json:"topQueries"`
	TargetKeywordTrend []TargetKeywordPoint `json:"targetKeywordTrend,omitempty"`
}

// ArticleDetailView builds one article's trend + top queries + target-keyword
// line over [from, to].
func ArticleDetailView(ctx context.Context, entityID primitive.ObjectID, wecIDs []primitive.ObjectID, articleID primitive.ObjectID, from, to, settled string) (*ArticleDetail, error) {
	hex := articleID.Hex()

	// Rollup row (title, totals, traffic value, demand capture) via the table
	// builder — one article's slice of the same joins.
	tableRows, err := ArticlesTable(ctx, entityID, wecIDs, from, to, settled)
	if err != nil {
		return nil, err
	}
	detail := &ArticleDetail{ArticleRow: ArticleRow{ArticleID: hex}}
	for _, row := range tableRows {
		if row.ArticleID == hex {
			detail.ArticleRow = row
			break
		}
	}

	// Daily trend across the article's pages.
	match := factMatch(entityID, gscsrc.GrainPage, from, to)
	match["dims."+gscsrc.DimArticleID] = hex
	var trendRows []struct {
		Date        string  `bson:"_id"`
		Clicks      float64 `bson:"clicks"`
		Impressions float64 `bson:"impressions"`
	}
	if err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": match},
		{"$group": bson.M{
			"_id":         "$date",
			"clicks":      bson.M{"$sum": "$metrics." + gscsrc.MetClicks},
			"impressions": bson.M{"$sum": "$metrics." + gscsrc.MetImpressions},
		}},
		{"$sort": bson.M{"_id": 1}},
	}, &trendRows); err != nil {
		return nil, err
	}
	for _, r := range trendRows {
		detail.Trend = append(detail.Trend, ArticleTrendPoint{Date: r.Date, Clicks: r.Clicks, Impressions: r.Impressions})
	}

	// Top queries.
	pqMatch := factMatch(entityID, gscsrc.GrainPageQuery, from, to)
	pqMatch["dims."+gscsrc.DimArticleID] = hex
	var queryRows []aggRow
	if err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": pqMatch},
		{"$group": sumStage("$dims." + gscsrc.DimQuery)},
		{"$sort": bson.M{"clicks": -1, "_id": 1}},
		{"$limit": topQueriesPerArticle},
	}, &queryRows); err != nil {
		return nil, err
	}
	for _, r := range queryRows {
		detail.TopQueries = append(detail.TopQueries, ArticleQueryRow{
			Query:       r.ID,
			Clicks:      r.Clicks,
			Impressions: r.Impressions,
			CTR:         derivedCTR(r.Clicks, r.Impressions),
			Position:    derivedPosition(r.PosWeighted, r.Impressions),
		})
	}

	// Target keyword per-day line.
	if detail.TargetKeyword != "" {
		targetText := strings.ToLower(strings.TrimSpace(detail.TargetKeyword))
		var lineRows []struct {
			ID struct {
				Date  string `bson:"date"`
				Query string `bson:"query"`
			} `bson:"_id"`
			Clicks      float64 `bson:"clicks"`
			Impressions float64 `bson:"impressions"`
			PosWeighted float64 `bson:"posWeighted"`
		}
		if err := models.AggregateAnalyticsFacts(ctx, []bson.M{
			{"$match": pqMatch},
			{"$group": sumStage(bson.M{
				"date":  "$date",
				"query": "$dims." + gscsrc.DimQuery,
			})},
		}, &lineRows); err != nil {
			return nil, err
		}
		for _, r := range lineRows {
			if strings.ToLower(strings.TrimSpace(r.ID.Query)) != targetText {
				continue
			}
			detail.TargetKeywordTrend = append(detail.TargetKeywordTrend, TargetKeywordPoint{
				Date:     r.ID.Date,
				Position: derivedPosition(r.PosWeighted, r.Impressions),
				Clicks:   r.Clicks,
			})
		}
		sort.Slice(detail.TargetKeywordTrend, func(i, j int) bool {
			return detail.TargetKeywordTrend[i].Date < detail.TargetKeywordTrend[j].Date
		})
	}

	return detail, nil
}

// ---- queries table ----

// QueryRow is one row of the site query table.
type QueryRow struct {
	Query       string  `json:"query"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
	// Tracked-keyword enrichment (present only when the query text matches a
	// stored keyword, lowercase-trimmed exact match).
	Tracked bool    `json:"tracked"`
	Volume  int     `json:"volume,omitempty"`
	CPC     float64 `json:"cpc,omitempty"`
	// DemandCapture: clicks over the fixed 28-day demand window ÷ Volume.
	DemandCapture *float64 `json:"demandCapture,omitempty"`
}

// QueriesTable builds the site query table over [from, to].
// strikingDistance filters to position 5–20 with an impressions floor, sorted
// by impressions (the "one push from page 1" list).
func QueriesTable(ctx context.Context, entityID primitive.ObjectID, wecIDs []primitive.ObjectID, from, to, settled string, strikingDistance bool) ([]QueryRow, error) {
	var rows []aggRow
	err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": factMatch(entityID, gscsrc.GrainQuery, from, to)},
		{"$group": sumStage("$dims." + gscsrc.DimQuery)},
	}, &rows)
	if err != nil {
		return nil, err
	}

	stats, err := models.FindKeywordStatsByWebEntityContexts(ctx, wecIDs)
	if err != nil {
		return nil, err
	}
	dFrom, dTo := demandWindow(settled)
	var demandRows []struct {
		ID     string  `bson:"_id"`
		Clicks float64 `bson:"clicks"`
	}
	if err := models.AggregateAnalyticsFacts(ctx, []bson.M{
		{"$match": factMatch(entityID, gscsrc.GrainQuery, dFrom, dTo)},
		{"$group": bson.M{"_id": "$dims." + gscsrc.DimQuery, "clicks": bson.M{"$sum": "$metrics." + gscsrc.MetClicks}}},
	}, &demandRows); err != nil {
		return nil, err
	}
	demandClicks := make(map[string]float64, len(demandRows))
	for _, r := range demandRows {
		demandClicks[r.ID] = r.Clicks
	}

	out := make([]QueryRow, 0, len(rows))
	for _, r := range rows {
		row := QueryRow{
			Query:       r.ID,
			Clicks:      r.Clicks,
			Impressions: r.Impressions,
			CTR:         derivedCTR(r.Clicks, r.Impressions),
			Position:    derivedPosition(r.PosWeighted, r.Impressions),
		}
		if kw, ok := stats[strings.ToLower(strings.TrimSpace(r.ID))]; ok {
			row.Tracked = true
			row.Volume = kw.Volume
			row.CPC = kw.CPC
			if kw.Volume > 0 {
				capture := demandClicks[r.ID] / float64(kw.Volume)
				row.DemandCapture = &capture
			}
		}
		if strikingDistance {
			if row.Position < strikingPositionMin || row.Position > strikingPositionMax ||
				row.Impressions < strikingMinImpressions {
				continue
			}
		}
		out = append(out, row)
	}

	if strikingDistance {
		sort.Slice(out, func(i, j int) bool { return out[i].Impressions > out[j].Impressions })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Clicks > out[j].Clicks })
	}
	if len(out) > tableRowCap {
		out = out[:tableRowCap]
	}
	return out, nil
}
