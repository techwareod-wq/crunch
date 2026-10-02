package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Staff invite statuses (D-011).
const (
	InviteStatusPending  = "pending"
	InviteStatusAccepted = "accepted"
	InviteStatusRevoked  = "revoked"
	InviteStatusExpired  = "expired"
)

// ErrInvitePending is returned by InsertStaffInvite when the email already has
// a pending invite (unique partial index).
var ErrInvitePending = errors.New("a pending invite already exists for this email")

// ErrInviteNotPending is returned by the status transitions when the invite
// has already left pending (accepted, revoked or expired).
var ErrInviteNotPending = errors.New("invite is no longer pending")

// StaffInvite is one "Invite staff" request (D-011): a Clerk invitation with
// the crunch role pre-attached. The role is applied when the invitee first
// signs in (Clerk user.created webhook or JWT auto-create, whichever is first).
type StaffInvite struct {
	ID                primitive.ObjectID `bson:"_id,omitempty"                 json:"id"`
	Email             string             `bson:"email"                         json:"email"` // lowercased
	Role              string             `bson:"role"                          json:"role"`
	ClerkInvitationID string             `bson:"clerk_invitation_id,omitempty" json:"clerkInvitationId,omitempty"`
	InvitedBy         string             `bson:"invited_by"                    json:"invitedBy"` // inviter email
	InvitedByUserID   string             `bson:"invited_by_user_id"            json:"invitedByUserId"`
	Status            string             `bson:"status"                        json:"status"`
	CreatedAt         time.Time          `bson:"created_at"                    json:"createdAt"`
	AcceptedAt        *time.Time         `bson:"accepted_at,omitempty"         json:"acceptedAt,omitempty"`
	AcceptedUserID    string             `bson:"accepted_user_id,omitempty"    json:"acceptedUserId,omitempty"`
	ClosedAt          *time.Time         `bson:"closed_at,omitempty"           json:"closedAt,omitempty"` // revoked/expired
}

// EnsureStaffInviteIndexes: at most one pending invite per email, plus the
// newest-first list.
func EnsureStaffInviteIndexes(ctx context.Context) error {
	_, err := Collection(staffInvitesCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "email", Value: 1}},
			Options: options.Index().
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"status": InviteStatusPending}),
		},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: fieldCreatedAt, Value: -1}}},
		{Keys: bson.D{{Key: fieldCreatedAt, Value: -1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure staff invite indexes: %w", err)
	}
	return nil
}

// InsertStaffInvite stores a new pending invite. A second pending invite for
// the same email returns ErrInvitePending.
func InsertStaffInvite(ctx context.Context, inv *StaffInvite) error {
	inv.Status = InviteStatusPending
	inv.CreatedAt = time.Now().UTC()
	id, err := InsertOne(ctx, staffInvitesCollection, inv)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrInvitePending
		}
		return err
	}
	inv.ID = id
	return nil
}

// SetStaffInviteClerkID records the Clerk invitation id after the Clerk call.
func SetStaffInviteClerkID(ctx context.Context, id primitive.ObjectID, clerkID string) error {
	return UpdateOne(ctx, staffInvitesCollection, bson.M{fieldID: id}, bson.M{"$set": bson.M{"clerk_invitation_id": clerkID}})
}

// DeleteStaffInvite removes an invite whose Clerk call failed, freeing the
// pending slot. Only used for that rollback.
func DeleteStaffInvite(ctx context.Context, id primitive.ObjectID) error {
	return DeleteOne(ctx, staffInvitesCollection, bson.M{fieldID: id})
}

// FindStaffInviteByID loads one invite.
func FindStaffInviteByID(ctx context.Context, id primitive.ObjectID) (bool, *StaffInvite, error) {
	var inv StaffInvite
	found, err := FindOne(ctx, staffInvitesCollection, bson.M{fieldID: id}, &inv)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &inv, nil
}

// FindPendingStaffInviteByEmail returns the pending invite for email (already
// lowercased by the caller), if any.
func FindPendingStaffInviteByEmail(ctx context.Context, email string) (bool, *StaffInvite, error) {
	var inv StaffInvite
	found, err := FindOne(ctx, staffInvitesCollection, bson.M{fieldEmail: email, "status": InviteStatusPending}, &inv)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &inv, nil
}

// ListStaffInvites pages invites newest-first, optionally by status. page is
// 1-based.
func ListStaffInvites(ctx context.Context, status string, page, limit int) ([]StaffInvite, int64, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	total, err := Collection(staffInvitesCollection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	opts := options.Find().
		SetSort(bson.D{{Key: fieldCreatedAt, Value: -1}, {Key: fieldID, Value: -1}}).
		SetSkip(int64(page-1) * int64(limit)).
		SetLimit(int64(limit))
	cur, err := Collection(staffInvitesCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	out := make([]StaffInvite, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// MarkStaffInviteAccepted moves a pending invite to accepted. Compare-and-set
// on status: a non-pending invite returns ErrInviteNotPending, which callers
// on the apply path treat as "someone else already finished".
func MarkStaffInviteAccepted(ctx context.Context, id primitive.ObjectID, userID string) error {
	now := time.Now().UTC()
	return transitionStaffInvite(ctx, id, bson.M{
		"status":           InviteStatusAccepted,
		"accepted_at":      now,
		"accepted_user_id": userID,
	})
}

// MarkStaffInviteClosed moves a pending invite to revoked or expired (CAS on
// status, like MarkStaffInviteAccepted).
func MarkStaffInviteClosed(ctx context.Context, id primitive.ObjectID, status string) error {
	return transitionStaffInvite(ctx, id, bson.M{
		"status":    status,
		"closed_at": time.Now().UTC(),
	})
}

func transitionStaffInvite(ctx context.Context, id primitive.ObjectID, set bson.M) error {
	res, err := Collection(staffInvitesCollection).UpdateOne(ctx,
		bson.M{fieldID: id, "status": InviteStatusPending},
		bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrInviteNotPending
	}
	return nil
}
