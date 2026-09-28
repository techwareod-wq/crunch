package models

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const subscriptionsCollection = "subscriptions"

// Internal subscription statuses (Paddle states mapped at webhook time).
// "expired" is never stored — readers derive it when valid_till has passed.
const (
	SubStatusActive    = "active"
	SubStatusTrialing  = "trialing"
	SubStatusCanceling = "canceling"
	SubStatusPastDue   = "past_due"
	SubStatusCanceled  = "canceled"
	SubStatusPaused    = "paused"
	SubStatusExpired   = "expired" // derived at read time only
)

// Subscription sources. Missing/empty means Paddle (every doc predates the
// field); "trial" is a card-less local trial created by start-trial — no real
// Paddle subscription exists behind its synthetic paddle_subscription_id, so
// service paths must branch before calling the Paddle API.
const (
	SubSourcePaddle = "paddle"
	SubSourceTrial  = "trial"
)

// Subscription is one document per Paddle subscription lifetime. A genuine
// resubscribe after expiry creates a new document; an un-cancel updates the
// existing one.
type Subscription struct {
	ID                   primitive.ObjectID `bson:"_id,omitempty"`
	UserID               primitive.ObjectID `bson:"user_id"`                // index (user_id, app_id, valid_till)
	AppID                string             `bson:"app_id,omitempty"`       // which app this sub belongs to; missing = Indexly (backfill stamps it)
	PaddleSubscriptionID string             `bson:"paddle_subscription_id"` // unique index; synthetic ("trial_...") for local trials
	PaddleCustomerID     string             `bson:"paddle_customer_id"`
	// Source distinguishes real Paddle subscriptions ("" / "paddle") from
	// card-less local trials ("trial"). See SubSource* constants.
	Source            string     `bson:"source,omitempty"`
	PriceID           string     `bson:"price_id"`
	ProductID         string     `bson:"product_id"`
	Status            string     `bson:"status"`
	ValidTill         time.Time  `bson:"valid_till"` // current_billing_period.ends_at
	ScheduledCancelAt *time.Time `bson:"scheduled_cancel_at,omitempty"`
	StartedAt         time.Time  `bson:"started_at,omitempty"`
	CanceledAt        *time.Time `bson:"canceled_at,omitempty"`
	// LastEventAt is the occurred_at of the newest applied webhook event.
	// Ordering guard: older events must never overwrite newer state.
	LastEventAt time.Time `bson:"last_event_at"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`

	// WasTrialing latches true the first time a trialing status is applied;
	// never unset.
	WasTrialing bool `bson:"was_trialing,omitempty"`

	// Trial lifecycle — two-marker pattern per event: *At records the fact
	// (set-once claim), *DispatchedAt records that the side-effect message
	// was sent. Redelivery re-dispatches when the fact is set but the
	// dispatch marker is not, so a dispatch failure heals on retry. There is
	// deliberately NO unclaim/rollback path (its own failure would lose the
	// event forever); dispatch is at-least-once, handlers idempotent.
	TrialConvertedAt           *time.Time `bson:"trial_converted_at,omitempty"`
	TrialConvertedDispatchedAt *time.Time `bson:"trial_converted_dispatched_at,omitempty"`
	TrialExpiredAt             *time.Time `bson:"trial_expired_at,omitempty"`
	TrialExpiredDispatchedAt   *time.Time `bson:"trial_expired_dispatched_at,omitempty"`

	// TrialFlagged marks a trialing sub created for an identity with prior
	// subscription history (re-trial attempt). Flag only — never blocks.
	// Surfaced in GET /v1/admin/trials.
	TrialFlagged bool `bson:"trial_flagged,omitempty"`

	// Quantity is the Paddle line-item quantity — the purchased seat count on
	// company-linked (team) subscriptions. Stamped by the webhook; <1 is
	// treated as 1. Individual subscriptions never carry it.
	Quantity int `bson:"quantity,omitempty"`
	// CompanyID links a team-billing subscription to the company it pays for
	// (custom_data.companyId at checkout). Individual and team subs share the
	// same app and Paddle customer (the owner), so EVERY individual-flow query
	// filters {company_id: {$exists: false}} — otherwise the owner's company
	// sub would block their personal checkout, be cancelable through the
	// individual cancel endpoint, and pollute status/trial logic. Only the
	// entitlement recompute derivation and the company-billing surface read
	// company subs, via the ByCompanyID finders below.
	CompanyID *primitive.ObjectID `bson:"company_id,omitempty"`
}

// ownSubFilter excludes company-linked (team) subscriptions — the predicate
// every "the user's own subscription" query must carry. See CompanyID above.
func ownSubFilter() bson.M {
	return bson.M{"$exists": false}
}

// appScopeFilter is the app_id predicate for subscription queries. Docs
// created before the multi-app split have no app_id until the backfill stamps
// it (and, between that backfill and the webhook-stamping deploy, new docs
// briefly lack it too) — missing always means Indexly, so the Indexly predicate
// must match absent values or every legacy subscriber would lose access the
// moment this binary deploys.
func appScopeFilter(appID string) interface{} {
	if appID == AppIDIndexly {
		return bson.M{"$in": bson.A{appID, nil}}
	}
	return appID
}

func FindLatestSubscriptionByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *Subscription, error) {
	return FindLatestSubscriptionByUserIDForApp(ctx, userID, AppIDIndexly)
}

// FindLatestSubscriptionByUserIDForApp returns the newest-valid_till
// subscription the user PERSONALLY holds for the app — the same winner rule
// the entitlement projection uses for the own-sub half of its derivation.
// Company-linked (team) subs are excluded: they are owner-keyed under the
// same user_id but belong to the company surface, not the individual flow.
func FindLatestSubscriptionByUserIDForApp(ctx context.Context, userID primitive.ObjectID, appID string) (bool, *Subscription, error) {
	var sub Subscription
	err := Collection(subscriptionsCollection).
		FindOne(ctx,
			bson.M{"user_id": userID, "app_id": appScopeFilter(appID), "company_id": ownSubFilter()},
			options.FindOne().SetSort(bson.D{{Key: "valid_till", Value: -1}})).
		Decode(&sub)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, &sub, nil
}

// FindLatestSubscriptionByCompanyID returns the newest-valid_till subscription
// linked to the company (team seat billing). Doc-keyed on company_id — the
// company checkout guard, billing summary, and cancel/resume all resolve the
// company's subscription through this, never through the owner's user_id.
func FindLatestSubscriptionByCompanyID(ctx context.Context, companyID primitive.ObjectID) (bool, *Subscription, error) {
	var sub Subscription
	err := Collection(subscriptionsCollection).
		FindOne(ctx,
			bson.M{"company_id": companyID},
			options.FindOne().SetSort(bson.D{{Key: "valid_till", Value: -1}})).
		Decode(&sub)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, &sub, nil
}

// FindLatestSubscriptionsByCompanyIDs returns, per company, the newest-
// valid_till subscription for the app — the company half of the entitlement
// recompute derivation (a member in two billed companies gets one candidate
// from each; the recompute picks the overall newest).
func FindLatestSubscriptionsByCompanyIDs(ctx context.Context, appID string, companyIDs []primitive.ObjectID) ([]Subscription, error) {
	if len(companyIDs) == 0 {
		return nil, nil
	}
	cur, err := Collection(subscriptionsCollection).Find(ctx,
		bson.M{"company_id": bson.M{"$in": companyIDs}, "app_id": appScopeFilter(appID)},
		options.Find().SetSort(bson.D{{Key: "valid_till", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var all []Subscription
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	// Sorted newest-first, so the first doc seen per company is its winner.
	seen := make(map[primitive.ObjectID]bool, len(companyIDs))
	out := make([]Subscription, 0, len(companyIDs))
	for i := range all {
		sub := all[i]
		if sub.CompanyID == nil || seen[*sub.CompanyID] {
			continue
		}
		seen[*sub.CompanyID] = true
		out = append(out, sub)
	}
	return out, nil
}

func FindSubscriptionByPaddleID(ctx context.Context, paddleSubscriptionID string) (bool, *Subscription, error) {
	var sub Subscription
	found, err := FindOne(ctx, subscriptionsCollection, bson.M{"paddle_subscription_id": paddleSubscriptionID}, &sub)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &sub, nil
}

// HasValidSubscription is the middleware fast path. Package-level var because
// middleware has no AppContext access (same pattern as the other models
// functions jwt.go calls directly) and tests swap it.
var HasValidSubscription = func(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	return HasValidSubscriptionForApp(ctx, userID, AppIDIndexly)
}

// HasValidSubscriptionForApp is one indexed count on
// {user_id, app_id, valid_till}. Paid-through rule: access iff
// now < valid_till, regardless of status (trialing counts).
func HasValidSubscriptionForApp(ctx context.Context, userID primitive.ObjectID, appID string) (bool, error) {
	n, err := Collection(subscriptionsCollection).CountDocuments(ctx, bson.M{
		"user_id":    userID,
		"app_id":     appScopeFilter(appID),
		"valid_till": bson.M{"$gt": time.Now().UTC()},
	}, options.Count().SetLimit(1))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// TrialRow is one row of the admin trials listing: the subscription plus the
// owning user's email (joined at query time).
type TrialRow struct {
	Subscription `bson:",inline"`
	UserEmail    string `bson:"-"`
}

// ListTrialingSubscriptions pages subscriptions currently in trialing status
// for the admin dashboard, newest trial end first, joined with user emails.
// appID == "" lists across apps; flaggedOnly restricts to re-trial-flagged
// rows. page is 1-based.
func ListTrialingSubscriptions(ctx context.Context, appID string, flaggedOnly bool, page, limit int) ([]TrialRow, int64, error) {
	filter := bson.M{"status": SubStatusTrialing}
	if appID != "" {
		filter["app_id"] = appScopeFilter(appID)
	}
	if flaggedOnly {
		filter["trial_flagged"] = true
	}

	total, err := Collection(subscriptionsCollection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "valid_till", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(int64(page-1) * int64(limit)).
		SetLimit(int64(limit))
	cur, err := Collection(subscriptionsCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	var rows []TrialRow
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}

	// Join emails in one $in query (tombstoned users included — a flagged
	// trial's previous identity may be deactivated).
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	if len(ids) > 0 {
		ucur, err := Collection(usersCollection).Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
		if err != nil {
			return nil, 0, err
		}
		var users []User
		if err := ucur.All(ctx, &users); err != nil {
			return nil, 0, err
		}
		emails := make(map[primitive.ObjectID]string, len(users))
		for _, u := range users {
			emails[u.ID] = u.Email
		}
		for i := range rows {
			rows[i].UserEmail = emails[rows[i].UserID]
		}
	}
	return rows, total, nil
}

// TrialEligibleIdentity reports whether userID's whole identity has no prior
// subscription history: the live user, tombstoned users sharing the email
// (the deactivation tombstone deliberately preserves them), and the same
// Paddle customer. Used by the webhook to FLAG re-trial attempts — never to
// block them (server-side offering is not enforcement; a modified client can
// check out against a trial price directly).
//
// excludePaddleSubID exempts the subscription being processed: its own doc
// may already exist (redelivery, later trialing events), and counting it
// would flag every trial as a re-trial.
func TrialEligibleIdentity(ctx context.Context, userID primitive.ObjectID, paddleCustomerID, excludePaddleSubID string) (bool, error) {
	ids := []primitive.ObjectID{userID}
	var u User
	found, err := FindOne(ctx, usersCollection, bson.M{"_id": userID}, &u)
	if err != nil {
		return false, err
	}
	if found && u.Email != "" {
		cur, err := Collection(usersCollection).Find(ctx, bson.M{"email": u.Email})
		if err != nil {
			return false, err
		}
		var sameEmail []User
		if err := cur.All(ctx, &sameEmail); err != nil {
			return false, err
		}
		for _, other := range sameEmail {
			if other.ID != userID {
				ids = append(ids, other.ID)
			}
		}
	}

	or := []bson.M{{"user_id": bson.M{"$in": ids}}}
	if paddleCustomerID != "" {
		or = append(or, bson.M{"paddle_customer_id": paddleCustomerID})
	}
	// Company-linked (team) subs ride the owner's user_id AND Paddle customer,
	// but buying seats for a team must not consume the owner's personal trial.
	filter := bson.M{"$or": or, "company_id": ownSubFilter()}
	if excludePaddleSubID != "" {
		filter["paddle_subscription_id"] = bson.M{"$ne": excludePaddleSubID}
	}
	n, err := Collection(subscriptionsCollection).CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// SetSubscriptionCanceling / SetSubscriptionActive are optimistic local writes
// after a successful Paddle API call. They intentionally do NOT advance
// last_event_at — the confirming subscription.updated webhook carries the
// authoritative state.
func SetSubscriptionCanceling(ctx context.Context, paddleSubscriptionID string, scheduledCancelAt *time.Time) error {
	set := bson.M{"status": SubStatusCanceling, "updated_at": time.Now().UTC()}
	if scheduledCancelAt != nil {
		set["scheduled_cancel_at"] = scheduledCancelAt.UTC()
	}
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		bson.M{"$set": set},
	)
	return err
}

// SetSubscriptionCanceled is the optimistic local write after an immediate
// Paddle cancellation. valid_till is clamped down with $min (never up), so the
// paid-through rule ends access at the cancellation moment — the same clamp the
// confirming subscription.canceled webhook applies.
func SetSubscriptionCanceled(ctx context.Context, paddleSubscriptionID string, canceledAt time.Time) error {
	at := canceledAt.UTC()
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		bson.M{
			"$set":   bson.M{"status": SubStatusCanceled, "canceled_at": at, "updated_at": time.Now().UTC()},
			"$min":   bson.M{"valid_till": at},
			"$unset": bson.M{"scheduled_cancel_at": ""},
		},
	)
	return err
}

func SetSubscriptionActive(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		bson.M{
			"$set":   bson.M{"status": SubStatusActive, "updated_at": time.Now().UTC()},
			"$unset": bson.M{"scheduled_cancel_at": ""},
		},
	)
	return err
}

// IsLocalTrial reports whether this doc is a card-less local trial — no real
// Paddle subscription exists behind it, so Paddle API calls must be skipped.
func (s *Subscription) IsLocalTrial() bool {
	return s.Source == SubSourceTrial
}

// LocalTrialSubscriptionID is the deterministic synthetic
// paddle_subscription_id of a user's local trial in an app. Determinism makes
// the unique index the one-trial-ever guard: a second start-trial (retry or
// race) hits a duplicate key instead of minting a second trial.
func LocalTrialSubscriptionID(userID primitive.ObjectID, appID string) string {
	return "trial_" + appID + "_" + userID.Hex()
}

// LocalTrialValidTill is the valid_till stamped on a card-less local trial.
// The free plan never lapses by date — only the lifetime article cap gates
// generation — so the horizon is effectively "forever". A far-future date
// (rather than a resolver special-case) keeps the expiry machinery, status
// derivation, and projections untouched.
func LocalTrialValidTill(now time.Time) time.Time {
	return now.UTC().AddDate(100, 0, 0)
}

// InsertTrialSubscription inserts a local trial doc. Duplicate-key errors on
// the unique paddle_subscription_id index are surfaced unwrapped (InsertCustomer
// idiom) so the service can map "trial already started" cleanly.
func InsertTrialSubscription(ctx context.Context, sub *Subscription) error {
	_, err := Collection(subscriptionsCollection).InsertOne(ctx, sub)
	return err
}

// SetSubscriptionTrialing restores trialing status (local-trial un-cancel).
// Local-trial counterpart of SetSubscriptionActive: a trial goes back to
// trialing, not active, and no webhook will reconcile the difference.
func SetSubscriptionTrialing(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		bson.M{
			"$set":   bson.M{"status": SubStatusTrialing, "updated_at": time.Now().UTC()},
			"$unset": bson.M{"scheduled_cancel_at": ""},
		},
	)
	return err
}

// SetTrialValidTill moves a local trial's end (admin extend). No Paddle call
// and no confirming webhook exists for source:"trial" — this write IS the
// authoritative state.
func SetTrialValidTill(ctx context.Context, paddleSubscriptionID string, validTill time.Time) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID},
		bson.M{"$set": bson.M{"valid_till": validTill.UTC(), "updated_at": time.Now().UTC()}},
	)
	return err
}

// EndLocalTrialsForUser terminates any live local trial for (user, app) — the
// supersede step when a real Paddle subscription lands mid-trial. Without it
// the trial doc would linger as "trialing" in the admin listing, and an
// extended trial whose valid_till outlasts the first billing period would win
// the newest-valid_till projection over the paid subscription. $min only
// clamps valid_till down, never up.
func EndLocalTrialsForUser(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error {
	at = at.UTC()
	_, err := Collection(subscriptionsCollection).UpdateMany(ctx,
		bson.M{
			"user_id": userID,
			"app_id":  appScopeFilter(appID),
			"source":  SubSourceTrial,
			"status":  bson.M{"$in": bson.A{SubStatusTrialing, SubStatusCanceling}},
		},
		bson.M{
			"$set":   bson.M{"status": SubStatusCanceled, "canceled_at": at, "updated_at": time.Now().UTC()},
			"$min":   bson.M{"valid_till": at},
			"$unset": bson.M{"scheduled_cancel_at": ""},
		},
	)
	return err
}

// ClaimTrialConversion records the set-once fact that a was_trialing
// subscription reached raw Paddle status "active" (trial → paid). The
// filter-guarded update makes the claim idempotent across redeliveries and
// a no-op for never-trialing or already-claimed subs — zero matches is
// success, not an error.
func ClaimTrialConversion(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{
			"paddle_subscription_id": paddleSubscriptionID,
			"was_trialing":           true,
			"trial_converted_at":     nil,
		},
		bson.M{"$set": bson.M{"trial_converted_at": time.Now().UTC()}},
	)
	return err
}

// ClaimTrialExpiry records the set-once fact that a was_trialing subscription
// ended (raw "canceled"/"paused") without ever converting. A conversion claim
// permanently blocks an expiry claim — the guard fields make the two facts
// mutually exclusive.
func ClaimTrialExpiry(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{
			"paddle_subscription_id": paddleSubscriptionID,
			"was_trialing":           true,
			"trial_converted_at":     nil,
			"trial_expired_at":       nil,
		},
		bson.M{"$set": bson.M{"trial_expired_at": time.Now().UTC()}},
	)
	return err
}

// MarkTrialConversionDispatched / MarkTrialExpiryDispatched stamp the second
// marker of the two-marker pattern. Best-effort at the call site: a lost
// stamp only means one duplicate dispatch on the next redelivery (handlers
// are idempotent). There is deliberately NO unclaim/rollback path.
func MarkTrialConversionDispatched(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID, "trial_converted_dispatched_at": nil},
		bson.M{"$set": bson.M{"trial_converted_dispatched_at": time.Now().UTC()}},
	)
	return err
}

func MarkTrialExpiryDispatched(ctx context.Context, paddleSubscriptionID string) error {
	_, err := Collection(subscriptionsCollection).UpdateOne(ctx,
		bson.M{"paddle_subscription_id": paddleSubscriptionID, "trial_expired_dispatched_at": nil},
		bson.M{"$set": bson.M{"trial_expired_dispatched_at": time.Now().UTC()}},
	)
	return err
}

// ApplySubscriptionEvent atomically upserts subscription state from a webhook
// event, guarded by last_event_at: the filter only matches when the event is
// newer than the doc, and a stale event's upsert attempt hits the unique
// paddle_subscription_id index instead. Returns applied=false (no error) when
// the event was stale.
func ApplySubscriptionEvent(ctx context.Context, paddleSubscriptionID string, occurredAt time.Time, update bson.M) (bool, error) {
	return applyGuardedUpsert(ctx, subscriptionsCollection, "paddle_subscription_id", paddleSubscriptionID, occurredAt, update)
}

// FindSubscriptionsByUserID returns every subscription doc the user holds,
// across all apps and statuses. Used by the admin deletion cascade, which must
// cancel and remove ALL of them — not just the entitlement winner.
func FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]Subscription, error) {
	cur, err := Collection(subscriptionsCollection).Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var subs []Subscription
	if err := cur.All(ctx, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

// DeleteSubscriptionsByUserID hard-deletes every subscription doc for the user.
// Admin deletion cascade only — callers must have already canceled the
// subscriptions at Paddle, written the deletedSubscriptions tombstones (webhook
// resurrection guard), and must recompute the entitlement projection afterwards.
// Zero matches is a no-op so re-runs converge.
func DeleteSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(subscriptionsCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
