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

const defaultUserRole = "user"

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
	// Role is the authoritative privilege axis (RBAC plan): it references a
	// Role.Key in the roles catalog. Defaults to "user" (no admin access).
	Role string `bson:"role" json:"role"`
	// ExtraGrants / ExtraRevokes are the per-user half of the hybrid RBAC model:
	// permission keys added to / removed from the role's set, honoring per-entry
	// expiry. They reuse the OverrideEntry shape (key/expires_at/by/at) already
	// defined for entitlements. Superuser-tier keys and admin.access are rejected
	// in grants at the API layer (authz containment guard), so a grant can never
	// escalate someone into staff or superuser powers.
	ExtraGrants  []OverrideEntry `bson:"extra_grants,omitempty"  json:"-"`
	ExtraRevokes []OverrideEntry `bson:"extra_revokes,omitempty" json:"-"`
	// RoleUpdatedAt is the optimistic-concurrency token for role assignment AND
	// grants edits (both write surfaces compare-and-set on it — RBAC plan §6).
	RoleUpdatedAt *time.Time `bson:"role_updated_at,omitempty" json:"role_updated_at,omitempty"`
	// LastActiveCompanyID is THE dashboard active-company source (tenancy plan
	// D9): resolved and membership-validated per request by the active-company
	// middleware (stale/absent → personal company, never a 403); updated only
	// by the switcher endpoint. Chat keeps its own binding-level state.
	LastActiveCompanyID *primitive.ObjectID `bson:"last_active_company_id,omitempty" json:"-"`
	CreatedAt           time.Time           `bson:"created_at"                  json:"created_at"`
	UpdatedAt           time.Time           `bson:"updated_at"                  json:"updated_at"`
	DeactivatedAt       *time.Time          `bson:"deactivated_at,omitempty"    json:"-"`
	// ScrubbedAt is the write-once PII-scrub guard: set the first time
	// DeactivateAndScrubUserByID rewrites the email/name, and filtered on by
	// re-runs so the deletion suffix is never appended to the email twice.
	ScrubbedAt *time.Time `bson:"scrubbed_at,omitempty" json:"-"`
	// Entitlements is keyed by app ID. Projection fields (Status, PriceID,
	// ValidTill, LastEventAt, Ver) are DERIVED from subscriptions — never edit
	// directly. Comp and Grants/Revokes are authoritative here (admin-edited).
	Entitlements map[string]AppEntitlement `bson:"entitlements,omitempty" json:"-"`
}

type AppEntitlement struct {
	// projection (derived; CAS-guarded writes — see RecomputeUserEntitlement)
	Status      string    `bson:"status,omitempty"`        // trialing | active | canceling | past_due | ...
	PriceID     string    `bson:"price_id,omitempty"`      // → tier via plans cache
	ValidTill   time.Time `bson:"valid_till,omitempty"`    // paid-through boundary
	LastEventAt time.Time `bson:"last_event_at,omitempty"` // observability: newest projected event
	Ver         int64     `bson:"ver"`                     // CAS counter for projection writes

	// overrides (admin-authoritative; entries can expire)
	Grants             []OverrideEntry `bson:"grants,omitempty"`
	Revokes            []OverrideEntry `bson:"revokes,omitempty"`
	OverridesUpdatedAt *time.Time      `bson:"overrides_updated_at,omitempty"` // optimistic concurrency

	// Comp is an admin-authored complimentary-access grant for the app:
	// while active (nil or future ValidTill) the resolver treats the user as
	// subscribed to the app's active paid tier even with no subscription
	// backing the projection. Lives beside Grants/Revokes as an
	// admin-authoritative field the projection recompute must NEVER wipe —
	// buildEntitlementProjectionUpdate touches dotted projection paths only.
	Comp *CompGrant `bson:"comp,omitempty"`
}

// CompGrant records who comped the app and until when (nil = indefinite).
type CompGrant struct {
	GrantedBy string     `bson:"granted_by"` // admin email (audit)
	GrantedAt time.Time  `bson:"granted_at"`
	ValidTill *time.Time `bson:"valid_till,omitempty"` // nil = no expiry
}

// Active reports whether the comp grants access at now.
func (c *CompGrant) Active(now time.Time) bool {
	if c == nil {
		return false
	}
	return c.ValidTill == nil || now.Before(*c.ValidTill)
}

type OverrideEntry struct {
	Key       string     `bson:"key"`
	ExpiresAt *time.Time `bson:"expires_at,omitempty"` // nil = no expiry
	By        string     `bson:"by"`                   // admin email (audit)
	At        time.Time  `bson:"at"`
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
		user.Role = defaultUserRole
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

// RecomputeUserEntitlement derives the projection for (user, app) from the
// subscriptions collection. Winner = newest valid_till for that app (existing
// multi-sub rule) among (a) the user's own non-company subs and (b) the subs
// of companies where the user holds a CLAIMED membership — team seat billing
// projects the company's subscription onto every claimed member (the owner
// included, via their own claimed membership). Zero matching subs → $unset
// projection fields, keep comp/grants/revokes. Idempotent.
//
// The write is a bounded COMPARE-AND-SET loop — NEVER an unconditional $set.
// Two concurrent recomputes interleaving read-then-write could otherwise
// persist a stale snapshot over a newer one (e.g. re-granting access a
// terminal cancel event had just clamped, with no later webhook to heal it).
// Losing the race retries with a fresh subscriptions read, so the retry
// observes every apply that preceded the competing projection write — the
// last writer always projects the freshest state.
//
// INVARIANT: every code path that writes the subscriptions collection calls
// RecomputeUserEntitlement afterwards. Call sites: webhook apply, optimistic
// local writes (cancel/resubscribe/Clerk-delete), backfill migration, admin
// recompute endpoint.
//
// Deactivated users are recomputed too (no activeFilter) — the projection is
// derived data and must stay consistent on tombstones.
func RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var u User
		found, err := FindOne(ctx, usersCollection, bson.M{fieldID: userID}, &u)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("recompute entitlement: user %s not found", userID.Hex())
		}

		var winner *Subscription
		if subFound, sub, err := FindLatestSubscriptionByUserIDForApp(ctx, userID, appID); err != nil {
			return err
		} else if subFound {
			winner = sub
		}

		// Team seat projection: claimed-membership company subs compete under
		// the same newest-valid_till rule. The winner's price_id maps to the
		// team tier through the plans cache, so everything downstream (CAS
		// write, dotted paths, resolver, feature gates) works unchanged.
		companyIDs, err := ListClaimedMembershipCompanyIDs(ctx, userID)
		if err != nil {
			return err
		}
		companySubs, err := FindLatestSubscriptionsByCompanyIDs(ctx, appID, companyIDs)
		if err != nil {
			return err
		}
		winner = pickEntitlementWinner(winner, companySubs)

		filter := entitlementCASFilter(userID, appID, u.Entitlements[appID].Ver)
		res, err := Collection(usersCollection).UpdateOne(ctx, filter, buildEntitlementProjectionUpdate(appID, winner))
		if err != nil {
			return err
		}
		if res.MatchedCount == 1 {
			return nil
		}
		lastErr = fmt.Errorf("recompute entitlement: lost CAS race for user %s app %s", userID.Hex(), appID)
	}
	// Webhook path: event marked failed → Paddle redelivers.
	return lastErr
}

// pickEntitlementWinner applies the newest-valid_till rule across the user's
// own subscription and their claimed companies' subscriptions. Ties keep the
// own sub (stable: a personal sub is not silently re-attributed to a company
// one of equal horizon). Pure — the unit-test target for the derivation.
func pickEntitlementWinner(own *Subscription, companySubs []Subscription) *Subscription {
	winner := own
	for i := range companySubs {
		if winner == nil || companySubs[i].ValidTill.After(winner.ValidTill) {
			winner = &companySubs[i]
		}
	}
	return winner
}

// entitlementCASFilter matches the user only while the projection version is
// still the one that was read. Ver is never stored as 0 ($inc creates it at
// 1), so ver==0 means the field was absent — matched via $in with null, which
// also covers an admin-created entitlement entry (comp/overrides) that has no
// ver yet.
func entitlementCASFilter(userID primitive.ObjectID, appID string, ver int64) bson.M {
	verKey := "entitlements." + appID + ".ver"
	if ver == 0 {
		return bson.M{fieldID: userID, verKey: bson.M{"$in": bson.A{int64(0), nil}}}
	}
	return bson.M{fieldID: userID, verKey: ver}
}

// buildEntitlementProjectionUpdate touches DOTTED PATHS ONLY so projection
// writes never clobber the admin-authoritative comp/grants/revokes fields
// living under the same app key. nil winner (zero subs) unsets the projection
// and keeps everything else.
func buildEntitlementProjectionUpdate(appID string, winner *Subscription) bson.M {
	base := "entitlements." + appID
	inc := bson.M{base + ".ver": 1}
	if winner == nil {
		return bson.M{
			"$unset": bson.M{
				base + ".status":        "",
				base + ".price_id":      "",
				base + ".valid_till":    "",
				base + ".last_event_at": "",
			},
			"$inc": inc,
		}
	}
	return bson.M{
		"$set": bson.M{
			base + ".status":        winner.Status,
			base + ".price_id":      winner.PriceID,
			base + ".valid_till":    winner.ValidTill.UTC(),
			base + ".last_event_at": winner.LastEventAt.UTC(),
		},
		"$inc": inc,
	}
}

// SetUserOverrides replaces the grants/revokes sets for (user, app). Admin
// path only; dotted paths so the projection and comp fields are never
// touched. Feature-key validation happens at the controller layer (unknown
// keys → 400) — this helper trusts its input. expected is the
// OverridesUpdatedAt the caller read (nil = never set); a mismatch returns
// ErrOverridesConflict so two admins can't silently drop each other's edits.
func SetUserOverrides(ctx context.Context, userID primitive.ObjectID, appID string,
	grants, revokes []OverrideEntry, expected *time.Time) error {
	base := "entitlements." + appID
	filter := bson.M{fieldID: userID}
	if expected != nil {
		filter[base+".overrides_updated_at"] = expected.UTC()
	} else {
		filter[base+".overrides_updated_at"] = nil // matches absent or null
	}

	now := time.Now().UTC()
	set := bson.M{base + ".overrides_updated_at": now}
	unset := bson.M{}
	if len(grants) > 0 {
		set[base+".grants"] = grants
	} else {
		unset[base+".grants"] = ""
	}
	if len(revokes) > 0 {
		set[base+".revokes"] = revokes
	} else {
		unset[base+".revokes"] = ""
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}

	res, err := Collection(usersCollection).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		// Distinguish a stale expectedUpdatedAt from a missing user.
		var u User
		found, ferr := FindOne(ctx, usersCollection, bson.M{fieldID: userID}, &u)
		if ferr != nil {
			return ferr
		}
		if !found {
			return fmt.Errorf("set overrides: user %s not found", userID.Hex())
		}
		return ErrOverridesConflict
	}
	return nil
}

// SetUserComp writes an admin comp grant for (user, app). Dotted path only —
// the projection and override fields under the same app key are untouched, so
// a concurrent recompute can't be clobbered and vice versa. Admin path;
// trusts its input.
func SetUserComp(ctx context.Context, userID primitive.ObjectID, appID string, comp CompGrant) error {
	res, err := Collection(usersCollection).UpdateOne(ctx,
		bson.M{fieldID: userID},
		bson.M{"$set": bson.M{
			"entitlements." + appID + ".comp": comp,
			fieldUpdatedAt:                    time.Now().UTC(),
		}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("set comp: user %s not found", userID.Hex())
	}
	return nil
}

// RemoveUserComp revokes the comp grant for (user, app). Idempotent — an
// absent comp unsets nothing and still succeeds.
func RemoveUserComp(ctx context.Context, userID primitive.ObjectID, appID string) error {
	res, err := Collection(usersCollection).UpdateOne(ctx,
		bson.M{fieldID: userID},
		bson.M{
			"$unset": bson.M{"entitlements." + appID + ".comp": ""},
			"$set":   bson.M{fieldUpdatedAt: time.Now().UTC()},
		},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("remove comp: user %s not found", userID.Hex())
	}
	return nil
}

// setUserRoleAxis is the shared compare-and-set write behind SetUserRole and
// SetUserGrants: both surfaces guard on role_updated_at so a concurrent role
// change and a concurrent grants edit can't silently clobber each other (RBAC
// plan §6). set/unset are the field mutations; role_updated_at + updated_at are
// always stamped. Distinguishes a stale precondition (ErrRoleConflictOnUser)
// from a missing user, mirroring SetUserOverrides.
func setUserRoleAxis(ctx context.Context, userID primitive.ObjectID, set bson.M, unset bson.M, expected *time.Time) error {
	filter := bson.M{fieldID: userID}
	if expected != nil {
		filter["role_updated_at"] = expected.UTC()
	} else {
		filter["role_updated_at"] = nil // matches absent or null
	}

	now := time.Now().UTC()
	set["role_updated_at"] = now
	set[fieldUpdatedAt] = now
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}

	res, err := Collection(usersCollection).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		var u User
		found, ferr := FindOne(ctx, usersCollection, bson.M{fieldID: userID}, &u)
		if ferr != nil {
			return ferr
		}
		if !found {
			return fmt.Errorf("set user role: user %s not found", userID.Hex())
		}
		return ErrRoleConflictOnUser
	}
	return nil
}

// SetUserRole assigns a role key with optimistic concurrency on
// role_updated_at. Privilege-escalation guards (rank ceiling, grant-only-what-
// you-hold, last-superuser) are enforced by the caller (authz + handler); this
// helper trusts its input.
func SetUserRole(ctx context.Context, userID primitive.ObjectID, role string, expected *time.Time) error {
	return setUserRoleAxis(ctx, userID, bson.M{"role": role}, nil, expected)
}

// SetUserGrants replaces the extra_grants / extra_revokes sets with optimistic
// concurrency on role_updated_at. Key validity (known, non-superuser-tier,
// admin.access forbidden in grants) is enforced at the controller layer.
func SetUserGrants(ctx context.Context, userID primitive.ObjectID, grants, revokes []OverrideEntry, expected *time.Time) error {
	set := bson.M{}
	unset := bson.M{}
	if len(grants) > 0 {
		set["extra_grants"] = grants
	} else {
		unset["extra_grants"] = ""
	}
	if len(revokes) > 0 {
		set["extra_revokes"] = revokes
	} else {
		unset["extra_revokes"] = ""
	}
	return setUserRoleAxis(ctx, userID, set, unset, expected)
}

// SetUserLastActiveCompany updates the dashboard active-company pointer (D9)
// — the switcher endpoint's only write. The caller has already validated the
// target membership is the user's own and claimed; the middleware re-validates
// on every later read, so a stale pointer degrades instead of lingering.
func SetUserLastActiveCompany(ctx context.Context, userID, companyID primitive.ObjectID) error {
	return UpdateOne(ctx, usersCollection, activeFilter(bson.M{fieldID: userID}), bson.M{"$set": bson.M{
		"last_active_company_id": companyID,
		fieldUpdatedAt:           time.Now().UTC(),
	}})
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
// clerk_id is deliberately KEPT: the SyncUser anti-resurrection guard keys on
// it, and the Clerk user is deleted upstream so the id can never return.
// transactions/adminActions keep resolving via the surviving _id.
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
