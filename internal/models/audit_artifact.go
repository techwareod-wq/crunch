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

const auditArtifactCollection = "auditArtifact"

// AuditArtifact is intermediate blackboard storage: one doc per (run, kind).
// Artifacts are scratch — the report embeds everything user-facing — so a TTL
// index prunes them after audit.artifactTTLDays.
type AuditArtifact struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	RunID     primitive.ObjectID `bson:"run_id"`
	Kind      string             `bson:"kind"`
	Payload   bson.Raw           `bson:"payload"` // one of audit/artifacts/types.go, bson-marshalled
	CreatedAt time.Time          `bson:"created_at"`
}

// EnsureAuditArtifactIndexes creates the unique (run, kind) index — what
// makes UpsertAuditArtifact redelivery-safe — and the TTL prune. Idempotent.
func EnsureAuditArtifactIndexes(ctx context.Context, ttl time.Duration) error {
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "run_id", Value: 1}, {Key: "kind", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "created_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(ttl.Seconds())),
		},
	}
	if _, err := Collection(auditArtifactCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure audit artifact indexes: %w", err)
	}
	return nil
}

// UpsertAuditArtifact writes one artifact payload, replacing any prior doc
// for (run, kind) — an SQS redelivery re-upserts the same normalized result.
func UpsertAuditArtifact(ctx context.Context, runID primitive.ObjectID, kind core.Kind, payload any) error {
	raw, err := bson.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s artifact: %w", kind, err)
	}
	_, err = Collection(auditArtifactCollection).UpdateOne(ctx,
		bson.M{"run_id": runID, "kind": string(kind)},
		bson.M{
			"$set":         bson.M{"payload": bson.Raw(raw)},
			"$setOnInsert": bson.M{"created_at": time.Now()},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("upsert %s artifact: %w", kind, err)
	}
	return nil
}

// LoadAuditArtifacts returns every artifact payload stored for the run,
// keyed by kind.
func LoadAuditArtifacts(ctx context.Context, runID primitive.ObjectID) (map[core.Kind]bson.Raw, error) {
	cur, err := Collection(auditArtifactCollection).Find(ctx, bson.M{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("load audit artifacts: %w", err)
	}
	defer cur.Close(ctx)

	out := map[core.Kind]bson.Raw{}
	for cur.Next(ctx) {
		var doc AuditArtifact
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode audit artifact: %w", err)
		}
		out[core.Kind(doc.Kind)] = doc.Payload
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit artifacts: %w", err)
	}
	return out, nil
}
