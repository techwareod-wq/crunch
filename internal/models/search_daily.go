package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SearchDay is a `search_daily` doc (spec 07): one local day's non-staff
// searches and enquiries in one country. Kept forever (D-106). _id is
// "YYYY-MM-DD|country".
type SearchDay struct {
	ID              string  `bson:"_id"                 json:"-"`
	Date            string  `bson:"date"                json:"date"`
	Country         string  `bson:"country"             json:"country"`
	Searches        int64   `bson:"searches"            json:"searches"`
	AISearches      int64   `bson:"ai_searches"         json:"aiSearches"`
	ZeroResult      int64   `bson:"zero_result"         json:"zeroResult"`
	FallbackUsed    int64   `bson:"fallback_used"       json:"fallbackUsed"`
	Expanded        int64   `bson:"expanded"            json:"expanded"`
	AIParseFailures int64   `bson:"ai_parse_failures"   json:"aiParseFailures"`
	AILatencyP50    float64 `bson:"ai_latency_p50"      json:"aiLatencyP50"`
	AILatencyP95    float64 `bson:"ai_latency_p95"      json:"aiLatencyP95"`
	// TopQueries / ZeroQueries keep the day's 200 most frequent.
	TopQueries          []QueryCount `bson:"top_queries"           json:"topQueries"`
	ZeroQueries         []QueryCount `bson:"zero_queries"          json:"zeroQueries"`
	Enquiries           int64        `bson:"enquiries"             json:"enquiries"`
	EnquiriesFromSearch int64        `bson:"enquiries_from_search" json:"enquiriesFromSearch"`
}

// QueryCount is one query's count in a day. Zero is its zero-result count
// (top queries); Place the searched place (zero-result queries).
type QueryCount struct {
	Q     string `bson:"q"               json:"q"`
	N     int64  `bson:"n"               json:"n"`
	Zero  int64  `bson:"zero,omitempty"  json:"zero,omitempty"`
	Place string `bson:"place,omitempty" json:"place,omitempty"`
}

// SearchDayID is the _id of one day + country.
func SearchDayID(date, country string) string { return date + "|" + country }

func searchDaily() *mongo.Collection { return Collection(searchDailyCollection) }

// EnsureSearchDailyIndexes: range reads by date.
func EnsureSearchDailyIndexes(ctx context.Context) error {
	if _, err := searchDaily().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "date", Value: 1}, {Key: "country", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure search_daily indexes: %w", err)
	}
	return nil
}

// ReplaceSearchDays writes one day's rollups: it upserts every row in rows
// and deletes that day's rows for countries no longer present (a re-run
// converges).
func ReplaceSearchDays(ctx context.Context, date string, rows []SearchDay) error {
	keep := bson.A{}
	for _, r := range rows {
		r.ID = SearchDayID(r.Date, r.Country)
		keep = append(keep, r.ID)
		if _, err := searchDaily().ReplaceOne(ctx, bson.M{"_id": r.ID}, r, options.Replace().SetUpsert(true)); err != nil {
			return err
		}
	}
	_, err := searchDaily().DeleteMany(ctx, bson.M{"date": date, "_id": bson.M{"$nin": keep}})
	return err
}

// ListSearchDays reads every rollup with date in [from, to] (YYYY-MM-DD).
func ListSearchDays(ctx context.Context, from, to string) ([]SearchDay, error) {
	return findAllDocs[SearchDay](ctx, searchDaily(), bson.M{"date": bson.M{"$gte": from, "$lte": to}},
		options.Find().SetSort(bson.D{{Key: "date", Value: 1}, {Key: "country", Value: 1}}))
}
