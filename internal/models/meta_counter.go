package models

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// WarehouseHub version counters (meta_counters, spec 02). Each is a single
// doc {_id: <name>, v: <int64>}; a missing doc reads as 0.
const (
	// CounterRulesVersion is bumped on every attribute-definition or industry
	// write; search projections record the version they were evaluated at.
	CounterRulesVersion = "rulesVersion"
	// CounterCatalogVersion is bumped on every publish / unpublish (04 map
	// cache key).
	CounterCatalogVersion = "catalogVersion"
)

type metaCounter struct {
	ID string `bson:"_id"`
	V  int64  `bson:"v"`
}

// GetCounter reads a counter (0 when it was never bumped).
func GetCounter(ctx context.Context, name string) (int64, error) {
	var c metaCounter
	found, err := FindOne(ctx, metaCountersCollection, bson.M{fieldID: name}, &c)
	if err != nil || !found {
		return 0, err
	}
	return c.V, nil
}

// BumpCounter atomically increments a counter and returns the new value.
func BumpCounter(ctx context.Context, name string) (int64, error) {
	var c metaCounter
	err := Collection(metaCountersCollection).FindOneAndUpdate(ctx,
		bson.M{fieldID: name},
		bson.M{"$inc": bson.M{"v": int64(1)}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&c)
	if err != nil {
		return 0, err
	}
	return c.V, nil
}

// CounterAnalyticsRolledThrough is the last local day (YYYYMMDD) the
// analytics rollup has written; the nightly run catches up from the day
// after it (spec 07).
const CounterAnalyticsRolledThrough = "analyticsRolledThrough"

// RaiseCounter sets a counter to v unless it already holds more.
func RaiseCounter(ctx context.Context, name string, v int64) error {
	_, err := Collection(metaCountersCollection).UpdateOne(ctx,
		bson.M{fieldID: name},
		bson.M{"$max": bson.M{"v": v}},
		options.Update().SetUpsert(true))
	return err
}
