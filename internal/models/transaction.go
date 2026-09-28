package models

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const transactionsCollection = "transactions"

const (
	TxnStatusCompleted = "completed"
	TxnStatusFailed    = "failed"
)

// Transaction is one document per Paddle transaction. Paddle retries failed
// payments on the SAME transaction ID, so rows are upserted (guarded by
// last_event_at), never insert-and-skip — a failed row must not block the
// later completed state of the same transaction.
type Transaction struct {
	ID                   primitive.ObjectID  `bson:"_id,omitempty"`
	PaddleTransactionID  string              `bson:"paddle_transaction_id"` // unique index
	SubscriptionID       *primitive.ObjectID `bson:"subscription_id,omitempty"`
	PaddleSubscriptionID string              `bson:"paddle_subscription_id,omitempty"`
	UserID               primitive.ObjectID  `bson:"user_id"` // index (user_id, created_at)
	Status               string              `bson:"status"`
	AmountTotal          string              `bson:"amount_total"` // minor units, string per Paddle
	CurrencyCode         string              `bson:"currency_code"`
	BilledAt             *time.Time          `bson:"billed_at,omitempty"`
	InvoiceNumber        string              `bson:"invoice_number,omitempty"`
	LastEventAt          time.Time           `bson:"last_event_at"`
	CreatedAt            time.Time           `bson:"created_at"`
	UpdatedAt            time.Time           `bson:"updated_at"`
}

func ListTransactionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]Transaction, error) {
	cur, err := Collection(transactionsCollection).Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var txns []Transaction
	if err := cur.All(ctx, &txns); err != nil {
		return nil, err
	}
	return txns, nil
}

// ApplyTransactionEvent is the same atomic guarded-upsert pattern keyed on
// paddle_transaction_id (failed → completed reuses the same ID).
func ApplyTransactionEvent(ctx context.Context, paddleTransactionID string, occurredAt time.Time, update bson.M) (bool, error) {
	return applyGuardedUpsert(ctx, transactionsCollection, "paddle_transaction_id", paddleTransactionID, occurredAt, update)
}
