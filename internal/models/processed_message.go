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

const processedMessagesCollection = "processed_messages"

// ProcessedMessage is the async-handler dedupe ledger row, keyed on the queue
// MessageID. ProcessedAt is nil while an attempt is in flight (or after it
// failed) and set once it completes — the distinction that lets retries
// reprocess but blocks duplicate at-least-once deliveries.
type ProcessedMessage struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	MessageID   string             `bson:"message_id"`  // unique index — dedupe key
	ReceivedAt  time.Time          `bson:"received_at"` // TTL index anchor
	ProcessedAt *time.Time         `bson:"processed_at,omitempty"`
	CreatedAt   time.Time          `bson:"created_at"` // when the row was first written
}

// EnsureProcessedMessageIndexes creates the unique dedupe index on message_id
// and a TTL index on received_at so the ledger self-prunes after ttl (which
// must exceed the longest window over which a message can be redelivered).
func EnsureProcessedMessageIndexes(ctx context.Context, ttl time.Duration) error {
	unique := true
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "message_id", Value: 1}},
			Options: &options.IndexOptions{Unique: &unique},
		},
		{
			Keys:    bson.D{{Key: "received_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(ttl.Seconds())),
		},
	}
	if _, err := Collection(processedMessagesCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure processed_messages indexes: %w", err)
	}
	return nil
}

// InsertProcessedMessage records a first sighting of msg.MessageID. On a
// duplicate message_id it returns duplicate=true plus the existing row so the
// caller can apply the dedupe rule (skip only if already processed). Mirrors
// InsertEvent.
func InsertProcessedMessage(ctx context.Context, msg *ProcessedMessage) (bool, *ProcessedMessage, error) {
	now := time.Now().UTC()
	msg.CreatedAt = now
	msg.ReceivedAt = now
	_, err := Collection(processedMessagesCollection).InsertOne(ctx, msg)
	if err == nil {
		return false, nil, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return false, nil, err
	}

	var existing ProcessedMessage
	found, ferr := FindOne(ctx, processedMessagesCollection, bson.M{"message_id": msg.MessageID}, &existing)
	if ferr != nil {
		return true, nil, ferr
	}
	if !found {
		// Raced with the TTL delete; treat as a fresh insert and reprocess.
		return false, nil, nil
	}
	return true, &existing, nil
}

// MarkProcessedMessage stamps processed_at so future deliveries of messageID
// are skipped. Mirrors MarkEventProcessed.
func MarkProcessedMessage(ctx context.Context, messageID string) error {
	_, err := Collection(processedMessagesCollection).UpdateOne(ctx,
		bson.M{"message_id": messageID},
		bson.M{"$set": bson.M{"processed_at": time.Now().UTC()}},
	)
	return err
}
