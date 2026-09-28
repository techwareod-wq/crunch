package models

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

const paddleEventsCollection = "paddle_events"

// PaddleEvent is the raw audit log and idempotency guard for webhook
// deliveries. Dedupe rule: a redelivery is skipped only when ProcessedAt is
// set; a row with a recorded error and no ProcessedAt is reprocessed.
type PaddleEvent struct {
	ID               primitive.ObjectID `bson:"_id,omitempty"`
	EventID          string             `bson:"event_id"` // unique index — dedupe key
	EventType        string             `bson:"event_type"`
	OccurredAt       time.Time          `bson:"occurred_at"`
	PaddleCustomerID string             `bson:"paddle_customer_id,omitempty"`
	// Payload is the raw webhook JSON body. Stored as a string — it is JSON
	// text, not BSON, so bson.Raw would produce an invalid document.
	Payload         string     `bson:"payload"`
	ReceivedAt      time.Time  `bson:"received_at"` // TTL index anchor
	ProcessedAt     *time.Time `bson:"processed_at,omitempty"`
	ProcessingError string     `bson:"processing_error,omitempty"`
}

// InsertEvent inserts the event row. On a duplicate event_id it returns
// duplicate=true plus the existing row so the caller can apply the refined
// dedupe rule (skip only if already processed).
func InsertEvent(ctx context.Context, event *PaddleEvent) (bool, *PaddleEvent, error) {
	event.ReceivedAt = time.Now().UTC()
	_, err := Collection(paddleEventsCollection).InsertOne(ctx, event)
	if err == nil {
		return false, nil, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return false, nil, err
	}

	var existing PaddleEvent
	found, ferr := FindOne(ctx, paddleEventsCollection, bson.M{"event_id": event.EventID}, &existing)
	if ferr != nil {
		return true, nil, ferr
	}
	if !found {
		// Raced with a concurrent delete (TTL); treat as fresh failure.
		return false, nil, err
	}
	return true, &existing, nil
}

// MarkEventProcessed stamps processed_at; note records a non-retryable
// resolution problem (e.g. unresolvable user) without triggering retries.
func MarkEventProcessed(ctx context.Context, eventID string, note string) error {
	update := bson.M{"$set": bson.M{"processed_at": time.Now().UTC()}}
	if note != "" {
		update["$set"].(bson.M)["processing_error"] = note
	} else {
		update["$unset"] = bson.M{"processing_error": ""}
	}
	_, err := Collection(paddleEventsCollection).UpdateOne(ctx, bson.M{"event_id": eventID}, update)
	return err
}

// MarkEventFailed records a transient processing error WITHOUT setting
// processed_at, so the Paddle redelivery is reprocessed.
func MarkEventFailed(ctx context.Context, eventID string, errMsg string) error {
	_, err := Collection(paddleEventsCollection).UpdateOne(ctx,
		bson.M{"event_id": eventID},
		bson.M{"$set": bson.M{"processing_error": errMsg}},
	)
	return err
}
