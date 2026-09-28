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

const deletedSubscriptionsCollection = "deletedSubscriptions"

// DeletedSubscription is a permanent tombstone for a subscription doc the
// admin deletion cascade hard-deleted. Its only job is the webhook
// resurrection guard: ApplySubscriptionEvent is an upsert, so a late-arriving
// Paddle event (canceled/updated queued before the cancel, delivered after the
// delete) would otherwise re-create the deleted doc. Tombstones are written
// BEFORE the doc delete, so there is no window in which an event can slip
// through. Paddle never reuses subscription ids, so rows stay valid forever.
type DeletedSubscription struct {
	ID                   primitive.ObjectID `bson:"_id,omitempty"`
	PaddleSubscriptionID string             `bson:"paddle_subscription_id"` // unique index
	UserID               primitive.ObjectID `bson:"user_id"`
	DeletedBy            string             `bson:"deleted_by"` // admin email (audit)
	DeletedAt            time.Time          `bson:"deleted_at"`
}

// EnsureDeletedSubscriptionIndexes creates the unique index on
// {paddle_subscription_id} that makes TombstoneSubscriptions upserts
// idempotent under cascade re-runs.
func EnsureDeletedSubscriptionIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(deletedSubscriptionsCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "paddle_subscription_id", Value: 1}},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure deleted subscription indexes: %w", err)
	}
	return nil
}

// TombstoneSubscriptions upserts one tombstone per subscription id. $setOnInsert
// keeps the first admin/timestamp on re-runs. Empty ids (defensive) are skipped.
func TombstoneSubscriptions(ctx context.Context, userID primitive.ObjectID, paddleSubIDs []string, by string) error {
	now := time.Now().UTC()
	for _, subID := range paddleSubIDs {
		if subID == "" {
			continue
		}
		upsert := true
		_, err := Collection(deletedSubscriptionsCollection).UpdateOne(ctx,
			bson.M{"paddle_subscription_id": subID},
			bson.M{"$setOnInsert": bson.M{
				"user_id":    userID,
				"deleted_by": by,
				"deleted_at": now,
			}},
			&options.UpdateOptions{Upsert: &upsert},
		)
		if err != nil {
			return fmt.Errorf("tombstone subscription %s: %w", subID, err)
		}
	}
	return nil
}

// IsSubscriptionTombstoned reports whether the subscription id was
// hard-deleted by the admin cascade. Called on the webhook apply path for
// every subscription event.
func IsSubscriptionTombstoned(ctx context.Context, paddleSubscriptionID string) (bool, error) {
	n, err := Collection(deletedSubscriptionsCollection).CountDocuments(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		options.Count().SetLimit(1))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
