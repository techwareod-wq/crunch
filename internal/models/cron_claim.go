package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CronClaim is the cron layer's exactly-once row: one per (job, occurrence).
// The first scheduler tick to insert it owns that occurrence cluster-wide —
// the ChatLease idea (exclusion lives in Mongo, not in-process) applied to a
// scheduled instant instead of a chat. CompletedAt is what makes a
// died-mid-dispatch run recoverable: a claim without it whose claimed_at has
// gone stale may be taken over and re-run, and the unit-level idempotency keys
// make the already-dispatched units no-ops.
type CronClaim struct {
	Job        string    `bson:"job"`
	Occurrence time.Time `bson:"occurrence"` // the truncated scheduled instant, UTC
	ClaimedAt  time.Time `bson:"claimed_at"`
	ClaimedBy  string    `bson:"claimed_by"` // hostname — for the run log
	// CompletedAt is set only after ALL units of the occurrence were dispatched.
	CompletedAt *time.Time `bson:"completed_at,omitempty"`
	ExpiresAt   time.Time  `bson:"expires_at"` // TTL anchor
}

const cronClaimsCollection = "cron_claims"

// cronClaimTTL bounds how long claim rows are kept for inspection ("did the
// 08:30 reminder fire on the 4th?"). Well past every CatchUp window.
const cronClaimTTL = 30 * 24 * time.Hour

// EnsureCronClaimIndexes creates the unique (job, occurrence) key the claim
// insert races against, and a TTL index reaping rows at their stamped
// expires_at (expireAfterSeconds 0 = delete at that instant).
func EnsureCronClaimIndexes(ctx context.Context) error {
	unique := true
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "job", Value: 1},
				{Key: "occurrence", Value: 1},
			},
			Options: &options.IndexOptions{Unique: &unique},
		},
		{
			Keys:    bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(0),
		},
	}
	if _, err := Collection(cronClaimsCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure cron_claims indexes: %w", err)
	}
	return nil
}

// AcquireCronClaim attempts to take the (job, occurrence) claim. Acquisition is
// "insert, OR take over an existing claim with no completed_at whose claimed_at
// is older than staleAfter" — the takeover is how an occurrence whose winner
// died mid-dispatch gets finished by a later tick. Returns acquired=false with
// a nil error when another node holds a live or completed claim.
func AcquireCronClaim(ctx context.Context, job string, occurrence time.Time, claimedBy string, staleAfter time.Duration) (bool, error) {
	now := time.Now().UTC()
	_, err := Collection(cronClaimsCollection).InsertOne(ctx, CronClaim{
		Job:        job,
		Occurrence: occurrence,
		ClaimedAt:  now,
		ClaimedBy:  claimedBy,
		ExpiresAt:  now.Add(cronClaimTTL),
	})
	if err == nil {
		return true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return false, fmt.Errorf("acquire cron claim %s@%s: %w", job, occurrence.Format(time.RFC3339), err)
	}

	// Somebody already took it. Re-take only an UNFINISHED claim gone stale —
	// a completed one means the occurrence is done for good.
	res, err := Collection(cronClaimsCollection).UpdateOne(ctx,
		bson.M{
			"job":          job,
			"occurrence":   occurrence,
			"completed_at": bson.M{"$exists": false},
			"claimed_at":   bson.M{"$lt": now.Add(-staleAfter)},
		},
		bson.M{"$set": bson.M{"claimed_at": now, "claimed_by": claimedBy}},
	)
	if err != nil {
		return false, fmt.Errorf("take over cron claim %s@%s: %w", job, occurrence.Format(time.RFC3339), err)
	}
	return res.ModifiedCount > 0, nil
}

// CompleteCronClaim stamps completed_at after every unit of the occurrence was
// dispatched — the marker that closes the takeover window.
func CompleteCronClaim(ctx context.Context, job string, occurrence time.Time) error {
	_, err := Collection(cronClaimsCollection).UpdateOne(ctx,
		bson.M{"job": job, "occurrence": occurrence},
		bson.M{"$set": bson.M{"completed_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("complete cron claim %s@%s: %w", job, occurrence.Format(time.RFC3339), err)
	}
	return nil
}
