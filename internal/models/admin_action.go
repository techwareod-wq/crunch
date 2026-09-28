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

const adminActionCollection = "adminActions"

// AdminAction is one row of the persisted admin audit trail: who did what to
// whom, and how the backend answered. Rows are written by
// middleware.WithAdminAuthorization — the one gate every /v1/admin/* route
// must pass — so curl calls are audited exactly like the dashboard. Mutating
// requests are always recorded; GET requests only when the allowlist denied
// them (an authenticated non-admin probing the admin surface).
type AdminAction struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	AdminEmail string             `bson:"admin_email" json:"adminEmail"`
	Method     string             `bson:"method" json:"method"`
	Path       string             `bson:"path" json:"path"`
	// ActionID is the dashboard registry id (X-Admin-Action header): a
	// client-supplied display label, never trusted for anything else.
	ActionID     string `bson:"action_id,omitempty" json:"actionId,omitempty"`
	TargetUserID string `bson:"target_user_id,omitempty" json:"targetUserId,omitempty"`
	// PayloadHash is the SHA-256 hex of the request payload. The raw payload
	// is deliberately never stored (it may carry user content).
	PayloadHash string    `bson:"payload_hash,omitempty" json:"payloadHash,omitempty"`
	Status      int       `bson:"status" json:"status"`
	CreatedAt   time.Time `bson:"created_at" json:"createdAt"`
}

// EnsureAdminActionIndexes backs the audit list's sort and its two filters.
// No TTL: an audit trail that expires is not an audit trail.
func EnsureAdminActionIndexes(ctx context.Context) error {
	_, err := Collection(adminActionCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}},
		{Keys: bson.D{{Key: "target_user_id", Value: 1}, {Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "admin_email", Value: 1}, {Key: "created_at", Value: -1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure admin action indexes: %w", err)
	}
	return nil
}

// InsertAdminAction appends one audit row. Append-only by design: no update
// or delete helpers exist for this collection.
func InsertAdminAction(ctx context.Context, action *AdminAction) error {
	action.CreatedAt = time.Now().UTC()
	id, err := InsertOne(ctx, adminActionCollection, action)
	if err != nil {
		return err
	}
	action.ID = id
	return nil
}

// ListAdminActions pages the audit trail newest-first, optionally filtered by
// target user and/or acting admin email. page is 1-based.
func ListAdminActions(ctx context.Context, targetUserID, adminEmail string, page, limit int) ([]AdminAction, int64, error) {
	filter := bson.M{}
	if targetUserID != "" {
		filter["target_user_id"] = targetUserID
	}
	if adminEmail != "" {
		filter["admin_email"] = adminEmail
	}

	total, err := Collection(adminActionCollection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(int64(page-1) * int64(limit)).
		SetLimit(int64(limit))
	cur, err := Collection(adminActionCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}

	actions := make([]AdminAction, 0, limit)
	if err := cur.All(ctx, &actions); err != nil {
		return nil, 0, err
	}
	return actions, total, nil
}
