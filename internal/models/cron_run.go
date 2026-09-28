package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CronRun is the cron observability ledger: one row per executed occurrence
// (plus one per admin force-run). It makes "did yesterday's digest reach
// everyone?" a query instead of an archaeology expedition — Candidates is what
// the resolver returned, Dispatched what actually reached the queue, Capped
// what a MaxUnits overflow dropped (logged, never silent).
type CronRun struct {
	Job        string    `bson:"job"`
	Occurrence time.Time `bson:"occurrence"`
	StartedAt  time.Time `bson:"started_at"`
	FinishedAt time.Time `bson:"finished_at"`
	Candidates int       `bson:"candidates"`
	Dispatched int       `bson:"dispatched"`
	Skipped    int       `bson:"skipped"`
	Capped     int       `bson:"capped"`
	Error      string    `bson:"error,omitempty"`
	ClaimedBy  string    `bson:"claimed_by"`
	// Forced marks an admin run-now execution (no claim taken).
	Forced    bool      `bson:"forced,omitempty"`
	ExpiresAt time.Time `bson:"expires_at"` // TTL anchor
}

const cronRunsCollection = "cron_runs"

// cronRunTTL keeps run rows for the same inspection window as claims.
const cronRunTTL = 30 * 24 * time.Hour

// EnsureCronRunIndexes creates the (job, occurrence) lookup index and the TTL
// reaper on expires_at.
func EnsureCronRunIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{Keys: bson.D{
			{Key: "job", Value: 1},
			{Key: "occurrence", Value: 1},
		}},
		{
			Keys:    bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(0),
		},
	}
	if _, err := Collection(cronRunsCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure cron_runs indexes: %w", err)
	}
	return nil
}

// InsertCronRun appends one run row. Best-effort by contract — callers log a
// failure rather than failing the occurrence over its own bookkeeping.
func InsertCronRun(ctx context.Context, run *CronRun) error {
	if run.ExpiresAt.IsZero() {
		run.ExpiresAt = time.Now().UTC().Add(cronRunTTL)
	}
	_, err := Collection(cronRunsCollection).InsertOne(ctx, run)
	if err != nil {
		return fmt.Errorf("insert cron run %s@%s: %w", run.Job, run.Occurrence.Format(time.RFC3339), err)
	}
	return nil
}
