package models

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// legacyUserValidTillIndex is the pre-multi-app subscriptions index replaced
// by (user_id, app_id, valid_till). Dropped by the backfill once app_id is
// stamped everywhere.
const legacyUserValidTillIndex = "user_id_1_valid_till_-1"

// EntitlementsBackfillReport summarizes one BackfillEntitlements run.
type EntitlementsBackfillReport struct {
	MissingAppID   int64    // subscription docs without app_id (stamp candidates)
	Stamped        int64    // docs actually stamped (apply only)
	UsersScanned   int      // distinct user_ids in subscriptions
	Drifted        int      // users whose projection didn't match the winner sub
	Recomputed     int      // projections rewritten (apply only)
	DriftedUserIDs []string // hex ids of drifted users (verbose reporting)
	Errors         []string // per-user failures; the run continues past them
}

// BackfillEntitlements is the entitlements migration behind
// cmd/entitlementsmigrate. Idempotent and re-runnable — it is deliberately
// drift-driven: a projection already matching its winner subscription is left
// untouched, so a second apply reports zero writes (that property is the
// Phase-5 cutover gate).
//
//  1. Stamp app_id: Indexly on every subscription missing it (docs predate the
//     multi-app split).
//  2. Swap the legacy (user_id, valid_till) index for
//     (user_id, app_id, valid_till).
//  3. For every distinct user_id in subscriptions, compare the stored
//     entitlements.indexly projection against the newest-valid_till subscription
//     and RecomputeUserEntitlement where they differ.
func BackfillEntitlements(ctx context.Context, dryRun bool) (*EntitlementsBackfillReport, error) {
	report := &EntitlementsBackfillReport{}

	// Step 1 — stamp app_id.
	missing, err := Collection(subscriptionsCollection).CountDocuments(ctx, bson.M{"app_id": bson.M{"$exists": false}})
	if err != nil {
		return report, fmt.Errorf("count subscriptions missing app_id: %w", err)
	}
	report.MissingAppID = missing
	if !dryRun && missing > 0 {
		res, err := Collection(subscriptionsCollection).UpdateMany(ctx,
			bson.M{"app_id": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"app_id": AppIDIndexly}},
		)
		if err != nil {
			return report, fmt.Errorf("stamp app_id: %w", err)
		}
		report.Stamped = res.ModifiedCount
	}

	// Step 2 — index swap (apply only; boot's EnsurePaymentIndexes also
	// creates the new one, so this just covers running before a deploy).
	if !dryRun {
		if err := swapSubscriptionUserIndex(ctx); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("index swap: %v", err))
		}
	}

	// Step 3 — recompute drifted projections.
	rawIDs, err := Collection(subscriptionsCollection).Distinct(ctx, "user_id", bson.M{})
	if err != nil {
		return report, fmt.Errorf("distinct subscription user_ids: %w", err)
	}
	for _, raw := range rawIDs {
		userID, ok := raw.(primitive.ObjectID)
		if !ok {
			report.Errors = append(report.Errors, fmt.Sprintf("non-ObjectID user_id in subscriptions: %v", raw))
			continue
		}
		report.UsersScanned++

		var winner *Subscription
		if found, sub, err := FindLatestSubscriptionByUserIDForApp(ctx, userID, AppIDIndexly); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("user %s: find winner: %v", userID.Hex(), err))
			continue
		} else if found {
			winner = sub
		}

		var u User
		found, err := FindOne(ctx, usersCollection, bson.M{"_id": userID}, &u)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("user %s: read: %v", userID.Hex(), err))
			continue
		}
		if !found {
			report.Errors = append(report.Errors, fmt.Sprintf("user %s: referenced by subscriptions but not in users", userID.Hex()))
			continue
		}

		if entitlementProjectionMatches(u.Entitlements[AppIDIndexly], winner) {
			continue
		}
		report.Drifted++
		report.DriftedUserIDs = append(report.DriftedUserIDs, userID.Hex())
		if dryRun {
			continue
		}
		if err := RecomputeUserEntitlement(ctx, userID, AppIDIndexly); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("user %s: recompute: %v", userID.Hex(), err))
			continue
		}
		report.Recomputed++
	}
	return report, nil
}

// entitlementProjectionMatches reports whether the stored projection already
// reflects the winner subscription (nil = no subscriptions for the app, which
// must leave the projection fields absent). Override fields are ignored —
// they are admin-authoritative, not derived.
func entitlementProjectionMatches(ent AppEntitlement, winner *Subscription) bool {
	if winner == nil {
		return ent.Status == "" && ent.PriceID == "" && ent.ValidTill.IsZero() && ent.LastEventAt.IsZero()
	}
	return ent.Status == winner.Status &&
		ent.PriceID == winner.PriceID &&
		ent.ValidTill.Equal(winner.ValidTill) &&
		ent.LastEventAt.Equal(winner.LastEventAt)
}

// swapSubscriptionUserIndex creates (user_id, app_id, valid_till) and drops
// the legacy (user_id, valid_till) index. Both halves are idempotent; a
// missing legacy index is not an error.
func swapSubscriptionUserIndex(ctx context.Context) error {
	_, err := Collection(subscriptionsCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "app_id", Value: 1}, {Key: "valid_till", Value: -1}},
	})
	if err != nil {
		return fmt.Errorf("create (user_id, app_id, valid_till): %w", err)
	}
	if _, err := Collection(subscriptionsCollection).Indexes().DropOne(ctx, legacyUserValidTillIndex); err != nil {
		var cmdErr mongo.CommandError
		if errors.As(err, &cmdErr) && cmdErr.Name == "IndexNotFound" {
			return nil
		}
		return fmt.Errorf("drop legacy index: %w", err)
	}
	return nil
}
