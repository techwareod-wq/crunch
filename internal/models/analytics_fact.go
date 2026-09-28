package models

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const analyticsFactCollection = "analyticsFacts"

// AnalyticsFact is one normalized analytics row: fixed indexed envelope + open
// maps (LLD §2.2). New metric/source = new map keys; zero struct or migration
// changes. The only tier the read APIs touch.
//
// Counts (clicks, impressions) live as float64 in Metrics; APIs cast on
// output. Aggregated CTR/position must be RECOMPUTED from sums at read time
// (sum(clicks)/sum(impressions), impression-weighted position) — never
// averaged from row values.
type AnalyticsFact struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	WebEntityID primitive.ObjectID `bson:"web_entity_id"`
	CompanyID   primitive.ObjectID `bson:"company_id,omitempty"`
	Source      string             `bson:"source"`
	// Grain: "site" | "page" | "query" | "page_query" | "domain_rating" | …
	Grain string `bson:"grain"`
	// Date is stored verbatim as the source reports it (see AnalyticsRaw.Date).
	Date string `bson:"date"`
	// DimKey is the canonical serialization of Dims ("" for dimensionless
	// grains) — Mongo can't put a unique index over an open map. Computed by
	// AnalyticsDimKey in the shared upsert path, never by normalizers.
	DimKey  string             `bson:"dim_key"`
	Dims    map[string]string  `bson:"dims,omitempty"`
	Metrics map[string]float64 `bson:"metrics"`
}

// encodeDimToken percent-encodes the three structural characters of the
// DimKey serialization. Page URLs legally contain '=' and '|', so plain
// joining would collide.
func encodeDimToken(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "=", "%3D")
	s = strings.ReplaceAll(s, "|", "%7C")
	return s
}

// AnalyticsDimKey serializes a dims map deterministically:
// sorted "enc(k)=enc(v)" pairs joined with "|"; "" for empty dims. The single
// source of truth for fact identity — the merge fold and the bulk upsert both
// go through here.
func AnalyticsDimKey(dims map[string]string) string {
	if len(dims) == 0 {
		return ""
	}
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, encodeDimToken(k)+"="+encodeDimToken(dims[k]))
	}
	return strings.Join(parts, "|")
}

// EnsureAnalyticsFactIndexes creates the unique upsert key
// {web_entity_id, source, grain, date, dim_key}, the main read index
// {web_entity_id, source, grain, date}, and the partial dotted-path index
// {web_entity_id, dims.article_id, date} for article rollups. Further dotted
// indexes (e.g. dims.page) are added only when a read path measurably needs
// them. Idempotent.
func EnsureAnalyticsFactIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "web_entity_id", Value: 1},
				{Key: "source", Value: 1},
				{Key: "grain", Value: 1},
				{Key: "date", Value: 1},
				{Key: "dim_key", Value: 1},
			},
			Options: options.Index().SetName("entity_source_grain_date_dimkey_unique").SetUnique(true),
		},
		{
			Keys: bson.D{
				{Key: "web_entity_id", Value: 1},
				{Key: "source", Value: 1},
				{Key: "grain", Value: 1},
				{Key: "date", Value: 1},
			},
			Options: options.Index().SetName("entity_source_grain_date_read"),
		},
		{
			Keys: bson.D{
				{Key: "web_entity_id", Value: 1},
				{Key: "dims.article_id", Value: 1},
				{Key: "date", Value: 1},
			},
			Options: options.Index().
				SetName("entity_article_date_partial").
				SetPartialFilterExpression(bson.M{"dims.article_id": bson.M{"$exists": true}}),
		},
	}
	if _, err := Collection(analyticsFactCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure analytics fact indexes: %w", err)
	}
	return nil
}

// BulkUpsertAnalyticsFacts upserts each fact on the unique
// (entity, source, grain, date, dim_key) identity in one unordered BulkWrite.
// DimKey is (re)computed here from Dims — the shared-helper guarantee that
// normalizers never hand-roll it. Callers pass MERGED facts (the collision
// fold has already run); a re-run of the same window replaces prior values.
func BulkUpsertAnalyticsFacts(ctx context.Context, facts []AnalyticsFact) error {
	if len(facts) == 0 {
		return nil
	}
	ops := make([]mongo.WriteModel, 0, len(facts))
	for _, f := range facts {
		f.DimKey = AnalyticsDimKey(f.Dims)
		set := bson.M{
			"company_id": f.CompanyID,
			"metrics":    f.Metrics,
		}
		if len(f.Dims) > 0 {
			set["dims"] = f.Dims
		}
		ops = append(ops, mongo.NewUpdateOneModel().
			SetFilter(bson.M{
				"web_entity_id": f.WebEntityID,
				"source":        f.Source,
				"grain":         f.Grain,
				"date":          f.Date,
				"dim_key":       f.DimKey,
			}).
			SetUpdate(bson.M{"$set": set}).
			SetUpsert(true))
	}
	if _, err := Collection(analyticsFactCollection).BulkWrite(ctx, ops,
		options.BulkWrite().SetOrdered(false)); err != nil {
		return fmt.Errorf("bulk upsert analytics facts: %w", err)
	}
	return nil
}

// AggregateAnalyticsFacts runs an aggregation pipeline over analyticsFacts and
// decodes every result into out (a pointer to a slice). The single read seam
// for the analytics read layer — metric definitions and table builders compose
// pipelines, this executes them.
func AggregateAnalyticsFacts(ctx context.Context, pipeline []bson.M, out any) error {
	stages := make(mongo.Pipeline, 0, len(pipeline))
	for _, stage := range pipeline {
		doc := bson.D{}
		for k, v := range stage {
			doc = append(doc, bson.E{Key: k, Value: v})
		}
		stages = append(stages, doc)
	}
	cursor, err := Collection(analyticsFactCollection).Aggregate(ctx, stages)
	if err != nil {
		return fmt.Errorf("aggregate analytics facts: %w", err)
	}
	defer cursor.Close(ctx)
	if err := cursor.All(ctx, out); err != nil {
		return fmt.Errorf("decode analytics facts aggregation: %w", err)
	}
	return nil
}

// DeleteAnalyticsFactsForRange removes one entity's facts for a source over an
// inclusive [from, to] date range — the replay delete-before-rebuild (facts
// whose dims vanish under new normalizer logic must not linger).
func DeleteAnalyticsFactsForRange(ctx context.Context, entityID primitive.ObjectID, source, from, to string) (int64, error) {
	res, err := Collection(analyticsFactCollection).DeleteMany(ctx, bson.M{
		"web_entity_id": entityID,
		"source":        source,
		"date":          bson.M{"$gte": from, "$lte": to},
	})
	if err != nil {
		return 0, fmt.Errorf("delete analytics facts for range: %w", err)
	}
	return res.DeletedCount, nil
}

// DeleteAnalyticsFactsByWebEntityIDs removes every fact belonging to the given
// entities — the account-deletion cascade step.
func DeleteAnalyticsFactsByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	if len(entityIDs) == 0 {
		return 0, nil
	}
	res, err := Collection(analyticsFactCollection).DeleteMany(ctx,
		bson.M{"web_entity_id": bson.M{"$in": entityIDs}})
	if err != nil {
		return 0, fmt.Errorf("delete analytics facts by web entity ids: %w", err)
	}
	return res.DeletedCount, nil
}
