package models

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const paddleCustomersCollection = "paddle_customers"

// PaddleCustomer maps one app user to one Paddle customer. Created by the
// checkout pre-create flow before any webhook arrives.
type PaddleCustomer struct {
	ID               primitive.ObjectID `bson:"_id,omitempty"`
	UserID           primitive.ObjectID `bson:"user_id"`            // unique index
	PaddleCustomerID string             `bson:"paddle_customer_id"` // unique index
	Email            string             `bson:"email"`
	CreatedAt        time.Time          `bson:"created_at"`
	UpdatedAt        time.Time          `bson:"updated_at"`
}

func FindCustomerByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *PaddleCustomer, error) {
	var c PaddleCustomer
	found, err := FindOne(ctx, paddleCustomersCollection, bson.M{"user_id": userID}, &c)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &c, nil
}

func FindCustomerByPaddleID(ctx context.Context, paddleCustomerID string) (bool, *PaddleCustomer, error) {
	var c PaddleCustomer
	found, err := FindOne(ctx, paddleCustomersCollection, bson.M{"paddle_customer_id": paddleCustomerID}, &c)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &c, nil
}

// InsertCustomer surfaces mongo duplicate-key errors unwrapped so callers can
// recover from concurrent pre-create races.
func InsertCustomer(ctx context.Context, customer *PaddleCustomer) error {
	now := time.Now().UTC()
	customer.CreatedAt = now
	customer.UpdatedAt = now
	_, err := Collection(paddleCustomersCollection).InsertOne(ctx, customer)
	return err
}

// DeletePaddleCustomersByUserID removes the user→Paddle-customer mapping(s).
// Admin delete-user cascade only; transactions are kept as the finance record.
// Zero matches is a no-op so re-runs converge.
func DeletePaddleCustomersByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(paddleCustomersCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// UpdateCustomerEmail refreshes the email on an existing mapping
// (customer.updated webhook). Missing mapping is a no-op.
func UpdateCustomerEmail(ctx context.Context, paddleCustomerID, email string) error {
	_, err := Collection(paddleCustomersCollection).UpdateOne(ctx,
		bson.M{"paddle_customer_id": paddleCustomerID},
		bson.M{"$set": bson.M{"email": email, "updated_at": time.Now().UTC()}},
	)
	return err
}
