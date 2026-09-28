package cron

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

// mongoStore backs the scheduler with the models-layer cron collections.
type mongoStore struct{}

// NewMongoStore returns the production Store (cron_claims + cron_runs).
func NewMongoStore() Store { return mongoStore{} }

func (mongoStore) AcquireClaim(ctx context.Context, job string, occ time.Time, claimedBy string, staleAfter time.Duration) (bool, error) {
	return models.AcquireCronClaim(ctx, job, occ, claimedBy, staleAfter)
}

func (mongoStore) CompleteClaim(ctx context.Context, job string, occ time.Time) error {
	return models.CompleteCronClaim(ctx, job, occ)
}

func (mongoStore) RecordRun(ctx context.Context, run RunRecord) error {
	return models.InsertCronRun(ctx, &models.CronRun{
		Job:        run.Job,
		Occurrence: run.Occurrence,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
		Candidates: run.Candidates,
		Dispatched: run.Dispatched,
		Skipped:    run.Skipped,
		Capped:     run.Capped,
		Error:      run.Error,
		ClaimedBy:  run.ClaimedBy,
		Forced:     run.Forced,
	})
}
