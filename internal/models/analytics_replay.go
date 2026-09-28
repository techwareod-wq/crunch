package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const analyticsReplayCollection = "analyticsReplays"

// Analytics replay statuses.
const (
	AnalyticsReplayStatusRunning = "running"
	AnalyticsReplayStatusDone    = "done"
	AnalyticsReplayStatusPartial = "partial"
)

// AnalyticsReplay tracks one admin-triggered re-normalization run (LLD §6):
// the endpoint mints the ID, enqueues one SQS unit per (entity, date-chunk),
// and each unit reports back here. Global bookkeeping, not per-user data — the
// account-deletion cascade deliberately leaves these alone.
type AnalyticsReplay struct {
	ID          primitive.ObjectID `bson:"_id"`
	Source      string             `bson:"source"`
	From        string             `bson:"from"`
	To          string             `bson:"to"`
	UnitsTotal  int                `bson:"units_total"`
	UnitsDone   int                `bson:"units_done"`
	UnitsFailed int                `bson:"units_failed"`
	Status      string             `bson:"status"`
	CreatedAt   time.Time          `bson:"created_at"`
}

// InsertAnalyticsReplay persists a freshly minted replay tracking doc.
func InsertAnalyticsReplay(ctx context.Context, replay AnalyticsReplay) error {
	if _, err := Collection(analyticsReplayCollection).InsertOne(ctx, replay); err != nil {
		return fmt.Errorf("insert analytics replay: %w", err)
	}
	return nil
}

// FindAnalyticsReplayByID fetches one replay tracking doc.
func FindAnalyticsReplayByID(ctx context.Context, id primitive.ObjectID) (bool, *AnalyticsReplay, error) {
	var replay AnalyticsReplay
	found, err := FindOne(ctx, analyticsReplayCollection, bson.M{"_id": id}, &replay)
	if err != nil || !found {
		return found, nil, err
	}
	return true, &replay, nil
}

// RecordAnalyticsReplayUnit increments the done or failed counter for one
// replay unit and derives the terminal status once every unit has reported
// (done + failed == total; any failure ⇒ partial). The two writes are not
// transactional — a crash between them leaves status "running", which an
// operator reads as "check the counters", never as false completion.
func RecordAnalyticsReplayUnit(ctx context.Context, id primitive.ObjectID, failed bool) error {
	field := "units_done"
	if failed {
		field = "units_failed"
	}
	var replay AnalyticsReplay
	err := Collection(analyticsReplayCollection).FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$inc": bson.M{field: 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&replay)
	if err != nil {
		return fmt.Errorf("record analytics replay unit: %w", err)
	}
	if replay.UnitsDone+replay.UnitsFailed < replay.UnitsTotal {
		return nil
	}
	status := AnalyticsReplayStatusDone
	if replay.UnitsFailed > 0 {
		status = AnalyticsReplayStatusPartial
	}
	if err := UpdateOne(ctx, analyticsReplayCollection, bson.M{"_id": id},
		bson.M{"$set": bson.M{"status": status}}); err != nil {
		return fmt.Errorf("finalize analytics replay status: %w", err)
	}
	return nil
}
