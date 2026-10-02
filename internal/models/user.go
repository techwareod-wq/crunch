package models

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureUserIndexes creates a unique index on clerk_id. Two concurrent paths
// (JWT stub creation and the Clerk webhook) can both pass a find-then-create
// check for the same clerk_id — the unique index makes the loser get a dup-key
// error so it can re-read the winner instead of inserting a second record.
// Idempotent.
func EnsureUserIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(usersCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "clerk_id", Value: 1}},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure user indexes: %w", err)
	}
	return nil
}

type User struct {
	ID      primitive.ObjectID `bson:"_id,omitempty"               json:"id"`
	ClerkID string             `bson:"clerk_id"                    json:"clerk_id"`
	Email   string             `bson:"email"                       json:"email"`
	Name    string             `bson:"name"                        json:"name"`
	// Role is user, admin or superuser (see internal/authz).
	Role string `bson:"role" json:"role"`
	// Permissions are an admin's assignable permissions: any of "editor",
	// "approver", "attributes". Ignored for any other role.
	Permissions []string `bson:"permissions,omitempty" json:"permissions,omitempty"`
	// RoleUpdatedAt is the optimistic-concurrency token for access edits.
	RoleUpdatedAt *time.Time `bson:"role_updated_at,omitempty" json:"role_updated_at,omitempty"`
	CreatedAt     time.Time  `bson:"created_at"                  json:"created_at"`
	UpdatedAt     time.Time  `bson:"updated_at"                  json:"updated_at"`
	DeactivatedAt *time.Time `bson:"deactivated_at,omitempty"    json:"-"`
	// ScrubbedAt is the write-once PII-scrub guard: set the first time
	// DeactivateAndScrubUserByID rewrites the email/name, and filtered on by
	// re-runs so the deletion suffix is never appended to the email twice.
	ScrubbedAt *time.Time `bson:"scrubbed_at,omitempty" json:"-"`

	// Visitor profile (D-100), set via POST /v1/me/profile and prefilled on the
	// enquiry form. Phone is what the visitor typed; PhoneE164 is the
	// normalised form. Format-checked only — no OTP.
	Phone            string     `bson:"phone,omitempty"              json:"phone,omitempty"`
	PhoneE164        string     `bson:"phone_e164,omitempty"         json:"phoneE164,omitempty"`
	Company          string     `bson:"company,omitempty"            json:"company,omitempty"`
	ProfileUpdatedAt *time.Time `bson:"profile_updated_at,omitempty" json:"profileUpdatedAt,omitempty"`
}

// activeFilter merges the always-on "not deactivated" predicate into a query.
// Mongo treats a missing field and an explicit null as equivalent under $eq:nil,
// so legacy rows without the field are correctly considered active.
func activeFilter(filter bson.M) bson.M {
	filter["deactivated_at"] = nil
	return filter
}

// FindUserByID returns the user only if they are not deactivated.
func FindUserByID(ctx context.Context, id string) (bool, *User, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, err
	}

	var user User
	found, err := FindOne(ctx, usersCollection, activeFilter(bson.M{fieldID: oid}), &user)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &user, nil
}

// FindUserByClerkID returns the user only if they are not deactivated.
// Use FindUserByClerkIDIncludingDeactivated when you need to inspect tombstones
// (webhook reconciliation, JWT bootstrap guard).
func FindUserByClerkID(ctx context.Context, clerkID string) (bool, *User, error) {
	var user User
	found, err := FindOne(ctx, usersCollection, activeFilter(bson.M{"clerk_id": clerkID}), &user)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &user, nil
}

// FindUserByClerkIDIncludingDeactivated returns the user regardless of deactivation
// status. Callers must check `user.DeactivatedAt != nil` before treating the result
// as a live account.
func FindUserByClerkIDIncludingDeactivated(ctx context.Context, clerkID string) (bool, *User, error) {
	var user User
	found, err := FindOne(ctx, usersCollection, bson.M{"clerk_id": clerkID}, &user)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &user, nil
}

// FindUserByIDIncludingDeactivated returns the user regardless of deactivation
// status. Callers must check `user.DeactivatedAt != nil` before treating the
// result as a live account (admin delete-user re-runs resolve tombstones).
func FindUserByIDIncludingDeactivated(ctx context.Context, id string) (bool, *User, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, err
	}

	var user User
	found, err := FindOne(ctx, usersCollection, bson.M{fieldID: oid}, &user)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &user, nil
}

// FindUserByEmail returns the user only if they are not deactivated.
func FindUserByEmail(ctx context.Context, email string) (bool, *User, error) {
	var user User
	found, err := FindOne(ctx, usersCollection, activeFilter(bson.M{fieldEmail: email}), &user)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &user, nil
}

// ListUsers returns one page of active users (newest first) plus the total
// match count, for the admin dashboard's landing table. page is 1-based.
// query, when non-empty, is matched case-insensitively against email and name
// (literal substring — regex metacharacters are escaped).
func ListUsers(ctx context.Context, page, limit int, query string) ([]User, int64, error) {
	filter := activeFilter(bson.M{})
	if query != "" {
		quoted := regexp.QuoteMeta(query)
		filter["$or"] = []bson.M{
			{fieldEmail: bson.M{"$regex": quoted, "$options": "i"}},
			{"name": bson.M{"$regex": quoted, "$options": "i"}},
		}
	}

	total, err := Collection(usersCollection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: fieldCreatedAt, Value: -1}, {Key: fieldID, Value: -1}}).
		SetSkip(int64(page-1) * int64(limit)).
		SetLimit(int64(limit))

	cur, err := Collection(usersCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}

	users := make([]User, 0, limit)
	if err := cur.All(ctx, &users); err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func CreateUser(ctx context.Context, user *User) error {
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now
	if user.Role == "" {
		user.Role = RoleUser
	}

	id, err := InsertOne(ctx, usersCollection, user)
	if err != nil {
		return err
	}
	user.ID = id
	return nil
}

// UpdateUser only matches active users; deactivated users are immutable.
func UpdateUser(ctx context.Context, id string, update bson.M) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return err
	}

	update[fieldUpdatedAt] = time.Now()

	return UpdateOne(ctx, usersCollection, activeFilter(bson.M{fieldID: oid}), bson.M{"$set": update})
}

// UpdateUserByClerkID only matches active users; an out-of-order Clerk webhook
// (e.g. a delayed user.updated arriving after user.deleted) cannot mutate a
// tombstoned record.
func UpdateUserByClerkID(ctx context.Context, clerkID string, update bson.M) error {
	update[fieldUpdatedAt] = time.Now()
	return UpdateOne(ctx, usersCollection, activeFilter(bson.M{"clerk_id": clerkID}), bson.M{"$set": update})
}

// SetUserAccess sets role + permissions with optimistic concurrency on
// role_updated_at (expected nil matches a user never edited). The rules on
// who may set what are the caller's (admin controller); this trusts its input.
// A stale token is ErrRoleConflictOnUser.
func SetUserAccess(ctx context.Context, userID primitive.ObjectID, role string, permissions []string, expected *time.Time) error {
	filter := activeFilter(bson.M{fieldID: userID})
	if expected != nil {
		filter["role_updated_at"] = expected.UTC()
	} else {
		filter["role_updated_at"] = nil // matches absent or null
	}
	now := time.Now().UTC()
	update := bson.M{"$set": bson.M{"role": role, "role_updated_at": now, fieldUpdatedAt: now}}
	if len(permissions) > 0 {
		update["$set"].(bson.M)["permissions"] = permissions
	} else {
		update["$unset"] = bson.M{"permissions": ""}
	}
	res, err := Collection(usersCollection).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		found, _, ferr := FindUserByIDIncludingDeactivated(ctx, userID.Hex())
		if ferr != nil {
			return ferr
		}
		if !found {
			return fmt.Errorf("set user access: user %s not found", userID.Hex())
		}
		return ErrRoleConflictOnUser
	}
	return nil
}

// DeactivateUserByClerkID stamps deactivated_at on the user without removing the
// row. Idempotent: if the user does not exist, or is already deactivated, it
// returns nil so webhook retries don't generate spurious errors.
func DeactivateUserByClerkID(ctx context.Context, clerkID string) error {
	now := time.Now()
	res, err := Collection(usersCollection).UpdateOne(
		ctx,
		activeFilter(bson.M{"clerk_id": clerkID}),
		bson.M{"$set": bson.M{
			"deactivated_at": now,
			fieldUpdatedAt:   now,
		}},
	)
	if err != nil {
		return err
	}
	_ = res
	return nil
}

// DeactivateAndScrubUserByID tombstones the user AND anonymizes PII, for the
// admin delete-user cascade. Two idempotent writes:
//  1. tombstone — matches active users only, so a stamp already placed by the
//     Clerk user.deleted webhook is preserved (zero-match is fine);
//  2. scrub — the original email is kept readable but retired by appending
//     "+<user-id-hex>+<RFC3339 deletion time>" (exact-match lookups and future
//     signups no longer collide with it), and the name is blanked. Filtered on
//     scrubbed_at being unset, so re-runs are zero-match no-ops and the suffix
//     is never appended twice.
//
// phone, phone_e164 and company are deliberately KEPT (soft delete, D-019):
// the visitor's contact details survive account deletion, like their
// enquiries.
//
// clerk_id is deliberately KEPT: the SyncUser anti-resurrection guard keys on
// it, and the Clerk user is deleted upstream so the id can never return.
// adminActions keep resolving via the surviving _id.
func DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error {
	now := time.Now()
	if _, err := Collection(usersCollection).UpdateOne(
		ctx,
		activeFilter(bson.M{fieldID: id}),
		bson.M{"$set": bson.M{
			"deactivated_at": now,
			fieldUpdatedAt:   now,
		}},
	); err != nil {
		return err
	}

	suffix := "+" + id.Hex() + "+" + now.UTC().Format(time.RFC3339)
	_, err := Collection(usersCollection).UpdateOne(
		ctx,
		bson.M{fieldID: id, "scrubbed_at": nil},
		bson.A{bson.M{"$set": bson.M{
			fieldEmail: bson.M{"$concat": bson.A{
				bson.M{"$ifNull": bson.A{"$" + fieldEmail, ""}},
				suffix,
			}},
			"name":         "Deleted User",
			"scrubbed_at":  now,
			fieldUpdatedAt: now,
		}}},
	)
	return err
}
