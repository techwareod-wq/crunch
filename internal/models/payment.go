package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsurePaymentIndexes creates all payment-collection indexes. The unique
// event_id index is the webhook dedupe guard — if index creation is skipped,
// dedupe silently degrades to processing every duplicate, so boot must fail
// on error (mirrors EnsureKeywordIndexes).
func EnsurePaymentIndexes(ctx context.Context) error {
	unique := true

	indexes := []struct {
		collection string
		model      mongo.IndexModel
	}{
		{paddleCustomersCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		}},
		{paddleCustomersCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "paddle_customer_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		}},
		{subscriptionsCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "paddle_subscription_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		}},
		// Replaces the pre-multi-app (user_id, valid_till) index; the
		// entitlements backfill drops the old one after stamping app_id.
		{subscriptionsCollection, mongo.IndexModel{
			Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "app_id", Value: 1}, {Key: "valid_till", Value: -1}},
		}},
		// Company-linked (team) subs only — partial so the overwhelmingly
		// individual collection pays nothing for the company lookup path.
		{subscriptionsCollection, mongo.IndexModel{
			Keys: bson.D{{Key: "company_id", Value: 1}, {Key: "valid_till", Value: -1}},
			Options: options.Index().
				SetName("company_subscription_lookup").
				SetPartialFilterExpression(bson.M{"company_id": bson.M{"$exists": true}}),
		}},
		{transactionsCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "paddle_transaction_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		}},
		{transactionsCollection, mongo.IndexModel{
			Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}},
		}},
		{paddleEventsCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "event_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		}},
		// paddle_events would otherwise grow unbounded (every delivery is
		// stored, including ignored types). 90 days covers audit/replay needs.
		{paddleEventsCollection, mongo.IndexModel{
			Keys:    bson.D{{Key: "received_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(90 * 24 * 60 * 60),
		}},
	}

	for _, idx := range indexes {
		if _, err := Collection(idx.collection).Indexes().CreateOne(ctx, idx.model); err != nil {
			return fmt.Errorf("ensure payment indexes (%s): %w", idx.collection, err)
		}
	}
	return nil
}

// applyGuardedUpsert performs the atomic ordering-guarded upsert:
//
//	filter: {key: id, last_event_at: {$lt: occurredAt}}
//
// New doc → filter matches nothing → upsert inserts (the key equality is
// materialized into the new doc). Existing doc with older last_event_at →
// updated. Existing doc with newer-or-equal last_event_at → upsert tries to
// insert, hits the unique index → stale event, skipped. Equal timestamps
// skip, which is safe because every apply writes full state.
// Never read-compare-write: concurrent deliveries of the same event must
// not double-apply.
func applyGuardedUpsert(ctx context.Context, collection, key, id string, occurredAt time.Time, update bson.M) (bool, error) {
	set, _ := update["$set"].(bson.M)
	if set == nil {
		set = bson.M{}
		update["$set"] = set
	}
	set["last_event_at"] = occurredAt.UTC()
	set["updated_at"] = time.Now().UTC()

	setOnInsert, _ := update["$setOnInsert"].(bson.M)
	if setOnInsert == nil {
		setOnInsert = bson.M{}
		update["$setOnInsert"] = setOnInsert
	}
	setOnInsert["created_at"] = time.Now().UTC()

	res, err := Collection(collection).UpdateOne(ctx,
		bson.M{key: id, "last_event_at": bson.M{"$lt": occurredAt.UTC()}},
		update,
		options.Update().SetUpsert(true),
	)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil // stale or concurrent older event — skip
		}
		return false, err
	}
	return res.MatchedCount > 0 || res.UpsertedCount > 0, nil
}
