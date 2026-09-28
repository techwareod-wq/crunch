package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const analyticsRawCollection = "analyticsRaw"

// AnalyticsRaw is the latest source-native payload for one (source, entity,
// date, pull) — the analytics engine's replay tier (LLD §2.1). The rolling
// re-fetch window upserts these, so a source's revised numbers replace prior
// payloads for the same date. "Immutable" means normalizers and APIs never
// write here; only fetchers do. Kept forever, no TTL.
//
// Pull is source-defined ("site" | "page" | "query" | "page_query" |
// "summary"); an oversized payload is CHUNKED by the fetcher into
// "page_query#0", "page_query#1", … — each chunk a fully valid, independently
// normalizable payload subset (never truncated: truncated JSON is
// un-replayable).
type AnalyticsRaw struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	Source      string             `bson:"source"`
	WebEntityID primitive.ObjectID `bson:"web_entity_id"`
	CompanyID   primitive.ObjectID `bson:"company_id,omitempty"`
	// Date is stored verbatim as the source reports it ("2026-08-21" — GSC
	// days are Pacific Time; monthly sources use first-of-month). All date
	// math lives in the analytics clock, never here.
	Date      string    `bson:"date"`
	Pull      string    `bson:"pull"`
	FetchedAt time.Time `bson:"fetched_at"`
	RowCount  int       `bson:"row_count"`
	Payload   bson.Raw  `bson:"payload"`
}

// EnsureAnalyticsRawIndexes creates the unique upsert key
// {source, web_entity_id, date, pull}. Idempotent.
func EnsureAnalyticsRawIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{
		Keys: bson.D{
			{Key: "source", Value: 1},
			{Key: "web_entity_id", Value: 1},
			{Key: "date", Value: 1},
			{Key: "pull", Value: 1},
		},
		Options: options.Index().SetName("source_entity_date_pull_unique").SetUnique(true),
	}
	if _, err := Collection(analyticsRawCollection).Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure analytics raw indexes: %w", err)
	}
	return nil
}

// UpsertAnalyticsRaws replaces-or-inserts each raw doc on the unique
// (source, entity, date, pull) key in one unordered BulkWrite — the rolling
// re-fetch window's self-heal write.
func UpsertAnalyticsRaws(ctx context.Context, raws []AnalyticsRaw) error {
	if len(raws) == 0 {
		return nil
	}
	ops := make([]mongo.WriteModel, 0, len(raws))
	for _, r := range raws {
		r.ID = primitive.NilObjectID
		ops = append(ops, mongo.NewReplaceOneModel().
			SetFilter(bson.M{
				"source":        r.Source,
				"web_entity_id": r.WebEntityID,
				"date":          r.Date,
				"pull":          r.Pull,
			}).
			SetReplacement(r).
			SetUpsert(true))
	}
	if _, err := Collection(analyticsRawCollection).BulkWrite(ctx, ops,
		options.BulkWrite().SetOrdered(false)); err != nil {
		return fmt.Errorf("bulk upsert analytics raw: %w", err)
	}
	return nil
}

// FindAnalyticsRawForRange lists one entity's raw docs for a source over an
// inclusive [from, to] date range — the normalize/replay read. Dates are the
// stored strings ("2006-01-02"-shaped), which compare lexicographically.
func FindAnalyticsRawForRange(ctx context.Context, entityID primitive.ObjectID, source, from, to string) ([]AnalyticsRaw, error) {
	cursor, err := Collection(analyticsRawCollection).Find(ctx, bson.M{
		"source":        source,
		"web_entity_id": entityID,
		"date":          bson.M{"$gte": from, "$lte": to},
	})
	if err != nil {
		return nil, fmt.Errorf("find analytics raw for range: %w", err)
	}
	defer cursor.Close(ctx)
	var raws []AnalyticsRaw
	if err := cursor.All(ctx, &raws); err != nil {
		return nil, fmt.Errorf("decode analytics raw for range: %w", err)
	}
	return raws, nil
}

// DeleteAnalyticsRawByWebEntityIDs removes every raw doc belonging to the
// given entities — the account-deletion cascade step.
func DeleteAnalyticsRawByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	if len(entityIDs) == 0 {
		return 0, nil
	}
	res, err := Collection(analyticsRawCollection).DeleteMany(ctx,
		bson.M{"web_entity_id": bson.M{"$in": entityIDs}})
	if err != nil {
		return 0, fmt.Errorf("delete analytics raw by web entity ids: %w", err)
	}
	return res.DeletedCount, nil
}
