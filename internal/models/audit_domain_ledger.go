package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const auditDomainLedgerCollection = "auditDomainLedger"

// auditDomainClaim is one week-scoped lead-run claim on a target domain. The
// _id ("lead:<domain>:<isoYearWeek>") IS the uniqueness rule: decision 16's
// "1 lead run per target domain per week globally" is the one cap where the
// count-then-insert race is worth closing (parallel throwaway-email
// requests), so it uses the unique-key + TTL primitive (processed_messages
// precedent) instead of a count query.
type auditDomainClaim struct {
	ID        string    `bson:"_id"`
	CreatedAt time.Time `bson:"created_at"`
}

// EnsureAuditDomainLedgerIndexes creates the TTL prune (14 days comfortably
// exceeds the 1-week claim window). Idempotent.
func EnsureAuditDomainLedgerIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{
		Keys:    bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(int32((14 * 24 * time.Hour).Seconds())),
	}
	if _, err := Collection(auditDomainLedgerCollection).Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure audit domain ledger indexes: %w", err)
	}
	return nil
}

// AuditDomainLedgerKey builds the claim id for a lead run on a domain in the
// ISO week containing `at`.
func AuditDomainLedgerKey(domain string, at time.Time) string {
	year, week := at.ISOWeek()
	return fmt.Sprintf("lead:%s:%d-W%02d", domain, year, week)
}

// ClaimAuditDomainWeek inserts the week claim. duplicate=true means another
// lead run already claimed this domain this week (the caller maps it to the
// domain-cooldown error).
func ClaimAuditDomainWeek(ctx context.Context, key string) (duplicate bool, err error) {
	_, err = Collection(auditDomainLedgerCollection).InsertOne(ctx, auditDomainClaim{
		ID:        key,
		CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		return false, nil
	}
	if mongo.IsDuplicateKeyError(err) {
		return true, nil
	}
	return false, fmt.Errorf("claim audit domain week: %w", err)
}
