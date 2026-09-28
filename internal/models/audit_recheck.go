package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

const auditRecheckCollection = "auditRecheck"

// Re-check status ladder (single async stage).
const (
	AuditRecheckStatusCreated  = 0
	AuditRecheckStatusRunning  = 1
	AuditRecheckStatusComplete = 2
	AuditRecheckStatusError    = 3
)

// Re-check verdicts. The verdict is a VERIFICATION OVERLAY rendered on top of
// the immutable report — it never mutates the report or its score (the
// spec-snapshot determinism contract; the full score refresh is the next
// weekly audit).
const (
	AuditRecheckVerdictFixed        = "fixed"
	AuditRecheckVerdictImproved     = "improved"
	AuditRecheckVerdictUnchanged    = "unchanged"
	AuditRecheckVerdictRegressed    = "regressed"
	AuditRecheckVerdictInconclusive = "inconclusive" // source unavailable — honesty rule, never guess
)

// AuditRecheckSide is one side of the before/after comparison: the original
// outcome's findings + score contribution, or the re-collected result.
type AuditRecheckSide struct {
	Findings []core.Finding `bson:"findings,omitempty" json:"findings,omitempty"`
	HasScore bool           `bson:"has_score" json:"hasScore"`
	Earned   float64        `bson:"earned,omitempty" json:"earned,omitempty"`
	Possible float64        `bson:"possible,omitempty" json:"possible,omitempty"`
	// Pages is the count of distinct affected pages across the findings.
	Pages int `bson:"pages,omitempty" json:"pages,omitempty"`
	// SpotCheck flags an after-side computed over the finding's affected
	// URLs only (never a full re-crawl) — the FE copy reads "spot-checked
	// N affected pages".
	SpotCheck bool `bson:"spot_check,omitempty" json:"spotCheck,omitempty"`
}

// AuditRecheck is one per-check re-verification. History is kept — it's the
// user's fix log.
type AuditRecheck struct {
	ID primitive.ObjectID `bson:"_id,omitempty"`
	// RunID is the (latest completed) run the re-check verifies against.
	RunID primitive.ObjectID `bson:"run_id"`
	// WebEntityID makes the tenancy check on reads a field compare.
	WebEntityID primitive.ObjectID `bson:"web_entity_id"`
	CheckID     string             `bson:"check_id"`
	Status      int                `bson:"status"`
	Verdict     string             `bson:"verdict,omitempty"`
	Before      *AuditRecheckSide  `bson:"before,omitempty"`
	After       *AuditRecheckSide  `bson:"after,omitempty"`
	// Note carries the inconclusive/error explanation shown to the user.
	Note  string `bson:"note,omitempty"`
	Error string `bson:"error,omitempty"`

	CreatedAt   time.Time  `bson:"created_at"`
	UpdatedAt   time.Time  `bson:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty"`
}

// EnsureAuditRecheckIndexes creates the re-check indexes. Idempotent. No TTL
// — the history is the user's fix log.
func EnsureAuditRecheckIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "run_id", Value: 1}, {Key: "check_id", Value: 1}, {Key: "created_at", Value: -1}}},
	}
	if _, err := Collection(auditRecheckCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure audit recheck indexes: %w", err)
	}
	return nil
}

func CreateAuditRecheck(ctx context.Context, rc *AuditRecheck) error {
	now := time.Now()
	rc.CreatedAt = now
	rc.UpdatedAt = now
	id, err := InsertOne(ctx, auditRecheckCollection, rc)
	if err != nil {
		return fmt.Errorf("create audit recheck: %w", err)
	}
	rc.ID = id
	return nil
}

func FindAuditRecheckByID(ctx context.Context, id string) (bool, *AuditRecheck, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid audit recheck ID: %w", err)
	}
	var rc AuditRecheck
	found, err := FindOne(ctx, auditRecheckCollection, bson.M{"_id": oid}, &rc)
	if err != nil {
		return false, nil, fmt.Errorf("find audit recheck: %w", err)
	}
	return found, &rc, nil
}

// FindLatestAuditRecheckForCheck returns the newest re-check for (run, check)
// — the cooldown/in-flight guard's read.
func FindLatestAuditRecheckForCheck(ctx context.Context, runID primitive.ObjectID, checkID string) (bool, *AuditRecheck, error) {
	var rc AuditRecheck
	err := Collection(auditRecheckCollection).FindOne(ctx,
		bson.M{"run_id": runID, "check_id": checkID},
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&rc)
	if err == mongo.ErrNoDocuments {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find latest audit recheck: %w", err)
	}
	return true, &rc, nil
}

// auditRecheckHistoryCap bounds the latest-per-check scan (a run has ≤ ~40
// checks and the daily cap bounds writes, so this is generous).
const auditRecheckHistoryCap = 500

// ListLatestAuditRechecksForRun returns the newest re-check PER CHECK for a
// run — the overlay the run view hydrates in one call.
func ListLatestAuditRechecksForRun(ctx context.Context, runID primitive.ObjectID) ([]AuditRecheck, error) {
	cur, err := Collection(auditRecheckCollection).Find(ctx,
		bson.M{"run_id": runID},
		options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(auditRecheckHistoryCap),
	)
	if err != nil {
		return nil, fmt.Errorf("list audit rechecks: %w", err)
	}
	defer cur.Close(ctx)
	var all []AuditRecheck
	if err := cur.All(ctx, &all); err != nil {
		return nil, fmt.Errorf("decode audit rechecks: %w", err)
	}
	seen := map[string]bool{}
	latest := make([]AuditRecheck, 0, len(all))
	for _, rc := range all { // newest-first — first hit per check wins
		if seen[rc.CheckID] {
			continue
		}
		seen[rc.CheckID] = true
		latest = append(latest, rc)
	}
	return latest, nil
}

// CountAuditRechecksForRunSince backs the per-run daily cap (attempts count —
// the collection cost was spent either way).
func CountAuditRechecksForRunSince(ctx context.Context, runID primitive.ObjectID, since time.Time) (int64, error) {
	n, err := Collection(auditRecheckCollection).CountDocuments(ctx, bson.M{
		"run_id":     runID,
		"created_at": bson.M{"$gte": since},
	})
	if err != nil {
		return 0, fmt.Errorf("count audit rechecks: %w", err)
	}
	return n, nil
}

// MarkAuditRecheckRunning flips a created/running re-check to Running (a
// redelivered message simply re-marks — the single stage is idempotent up to
// its terminal write).
func MarkAuditRecheckRunning(ctx context.Context, id string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit recheck ID: %w", err)
	}
	return UpdateOne(ctx, auditRecheckCollection, bson.M{"_id": oid, "status": bson.M{"$lt": AuditRecheckStatusComplete}}, bson.M{
		"$set": bson.M{"status": AuditRecheckStatusRunning, "updated_at": time.Now()},
	})
}

// CompleteAuditRecheck writes the terminal verdict + after side. Note carries
// the inconclusive explanation when the source was unavailable.
func CompleteAuditRecheck(ctx context.Context, id, verdict, note string, after *AuditRecheckSide) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit recheck ID: %w", err)
	}
	now := time.Now()
	return UpdateOne(ctx, auditRecheckCollection, bson.M{"_id": oid}, bson.M{
		"$set": bson.M{
			"status":       AuditRecheckStatusComplete,
			"verdict":      verdict,
			"note":         note,
			"after":        after,
			"completed_at": now,
			"updated_at":   now,
		},
	})
}

// SetAuditRecheckError flips a re-check to the terminal error state (infra
// failure — distinct from an honest inconclusive verdict).
func SetAuditRecheckError(ctx context.Context, id, msg string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit recheck ID: %w", err)
	}
	now := time.Now()
	return UpdateOne(ctx, auditRecheckCollection, bson.M{"_id": oid}, bson.M{
		"$set": bson.M{
			"status":       AuditRecheckStatusError,
			"error":        msg,
			"completed_at": now,
			"updated_at":   now,
		},
	})
}
