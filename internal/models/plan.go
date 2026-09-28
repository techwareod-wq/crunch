package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const plansCollection = "plans"

// Plan is one (app, tier) entry of the plan catalog. Money (prices, billing
// intervals) stays in Paddle; a plan doc only maps Paddle price_ids to an app
// and its feature lists. Every price_id must be globally unique across plan
// docs — the plans cache inverts them into a price_id → plan map, and the
// webhook derives app_id from that inversion. A plan doc must exist BEFORE its
// Paddle price goes sellable: the webhook fails closed on unmapped prices.
//
// JSON tags mirror the bson names so the seed file (cmd/entitlementsmigrate
// -seed-plans) reads the same shape mongo stores.
type Plan struct {
	ID       primitive.ObjectID     `bson:"_id,omitempty"  json:"id,omitempty"`
	AppID    string                 `bson:"app_id"         json:"app_id"`
	Tier     string                 `bson:"tier"           json:"tier"` // e.g. "starter", "pro"
	Name     string                 `bson:"name"           json:"name"`
	Variants map[string]PlanVariant `bson:"variants"       json:"variants"` // "monthly" | "yearly"
	Features []string               `bson:"features"       json:"features"` // paid feature keys
	// TrialFeatures is the reduced set while trialing.
	TrialFeatures []string `bson:"trial_features" json:"trial_features"`
	// TrialDays > 0 offers a card-less local trial of this length: checkout is
	// skipped and POST /v1/payments/start-trial writes a source:"trial"
	// subscription instead. 0 = no card-less trial (Paddle checkout only).
	TrialDays int `bson:"trial_days,omitempty" json:"trial_days,omitempty"`
	// Active gates purchasability (checkout/plan listings) ONLY — never
	// resolution. A deactivated plan keeps resolving features for its
	// existing subscribers; deactivation must not silently strip access.
	Active    bool      `bson:"active"     json:"active"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

type PlanVariant struct {
	PriceID string `bson:"price_id" json:"price_id"`
	// No TrialPriceID: trials are detected by subscription STATUS, not by a
	// distinct price (Paddle trials run on the same price via trial settings).
}

// PriceIDs returns the plan's price ids across variants, order-stable enough
// for validation and cache inversion (map iteration order is irrelevant to
// both).
func (p *Plan) PriceIDs() []string {
	ids := make([]string, 0, len(p.Variants))
	for _, v := range p.Variants {
		if v.PriceID != "" {
			ids = append(ids, v.PriceID)
		}
	}
	return ids
}

// EnsurePlanIndexes creates the unique (app_id, tier) index. Boot fails on
// error like the other Ensure helpers — without it two admin writes could
// race duplicate tier docs.
func EnsurePlanIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(plansCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "app_id", Value: 1}, {Key: "tier", Value: 1}},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure plan indexes: %w", err)
	}
	return nil
}

// ListAllPlans returns every plan doc INCLUDING inactive ones — the cache
// must keep resolving features for subscribers of deactivated plans.
func ListAllPlans(ctx context.Context) ([]Plan, error) {
	cur, err := Collection(plansCollection).Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	var plans []Plan
	if err := cur.All(ctx, &plans); err != nil {
		return nil, err
	}
	return plans, nil
}

// ListPlansByApp returns an app's plan docs (admin listing).
func ListPlansByApp(ctx context.Context, appID string) ([]Plan, error) {
	cur, err := Collection(plansCollection).Find(ctx, bson.M{"app_id": appID},
		options.Find().SetSort(bson.D{{Key: "tier", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var plans []Plan
	if err := cur.All(ctx, &plans); err != nil {
		return nil, err
	}
	return plans, nil
}

func FindPlanByAppTier(ctx context.Context, appID, tier string) (bool, *Plan, error) {
	var plan Plan
	found, err := FindOne(ctx, plansCollection, bson.M{"app_id": appID, "tier": tier}, &plan)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &plan, nil
}

// UpsertPlan writes a plan doc keyed by (app_id, tier) — the seed step and
// admin plan writes both funnel through it, so re-running either is
// idempotent. Returns (created, changed).
func UpsertPlan(ctx context.Context, p *Plan) (bool, bool, error) {
	now := time.Now().UTC()
	res, err := Collection(plansCollection).UpdateOne(ctx,
		bson.M{"app_id": p.AppID, "tier": p.Tier},
		bson.M{
			"$set": bson.M{
				"name":           p.Name,
				"variants":       p.Variants,
				"features":       p.Features,
				"trial_features": p.TrialFeatures,
				"trial_days":     p.TrialDays,
				"active":         p.Active,
				"updated_at":     now,
			},
			"$setOnInsert": bson.M{"created_at": now},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return false, false, err
	}
	return res.UpsertedCount > 0, res.ModifiedCount > 0, nil
}

// PlanSeedReport summarizes one SeedPlans run.
type PlanSeedReport struct {
	Created   int
	Updated   int
	Unchanged int
}

// SeedPlans idempotently upserts a plan catalog keyed by (app_id, tier) —
// the cmd/entitlementsmigrate -seed-plans step. Validation runs over the
// MERGED view (incoming docs + existing docs they don't replace) so a seed
// can never introduce a price_id collision with another app's plans. Docs
// identical to what's stored are skipped, so a second apply reports zero
// writes.
func SeedPlans(ctx context.Context, incoming []Plan, dryRun bool) (*PlanSeedReport, error) {
	existing, err := ListAllPlans(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*Plan, len(existing))
	for i := range existing {
		byKey[existing[i].AppID+"/"+existing[i].Tier] = &existing[i]
	}

	merged := append([]Plan{}, incoming...)
	replaced := map[string]bool{}
	for _, p := range incoming {
		replaced[p.AppID+"/"+p.Tier] = true
	}
	for _, p := range existing {
		if !replaced[p.AppID+"/"+p.Tier] {
			merged = append(merged, p)
		}
	}
	if err := ValidatePlanPriceUniqueness(merged); err != nil {
		return nil, err
	}

	report := &PlanSeedReport{}
	if !dryRun {
		if err := EnsurePlanIndexes(ctx); err != nil {
			return nil, err
		}
	}
	for i := range incoming {
		p := &incoming[i]
		cur, exists := byKey[p.AppID+"/"+p.Tier]
		switch {
		case exists && planSpecEqual(cur, p):
			report.Unchanged++
			continue
		case exists:
			report.Updated++
		default:
			report.Created++
		}
		if dryRun {
			continue
		}
		if _, _, err := UpsertPlan(ctx, p); err != nil {
			return report, fmt.Errorf("upsert plan %s/%s: %w", p.AppID, p.Tier, err)
		}
	}
	return report, nil
}

// planSpecEqual compares the seedable fields (everything but timestamps/id).
// nil and empty slices are equal — a JSON [] and an absent mongo field mean
// the same thing.
func planSpecEqual(a, b *Plan) bool {
	if a.Name != b.Name || a.Active != b.Active || a.TrialDays != b.TrialDays {
		return false
	}
	if len(a.Variants) != len(b.Variants) {
		return false
	}
	for k, v := range a.Variants {
		if b.Variants[k] != v {
			return false
		}
	}
	return stringSlicesEqual(a.Features, b.Features) &&
		stringSlicesEqual(a.TrialFeatures, b.TrialFeatures)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ValidatePlanPriceUniqueness rejects a plan set where any price_id appears
// twice (within one doc's variants or across docs) or any doc is structurally
// unusable. The cache re-checks on every load; this front-runs it at
// seed/admin-write time so invalid state never lands in the collection.
func ValidatePlanPriceUniqueness(plans []Plan) error {
	seen := map[string]string{} // price_id → "app/tier" that claimed it
	for i := range plans {
		p := &plans[i]
		if p.AppID == "" || p.Tier == "" {
			return fmt.Errorf("plan %q/%q: app_id and tier are required", p.AppID, p.Tier)
		}
		ids := p.PriceIDs()
		if len(ids) == 0 {
			// A virtual, resolution-only plan (e.g. trial_expired) sells nothing
			// and so carries no price — legal only while inactive, since Active
			// gates purchasability and a purchasable plan must be checkout-able.
			if p.Active {
				return fmt.Errorf("plan %s/%s: at least one variant with a price_id is required for an active plan", p.AppID, p.Tier)
			}
			continue
		}
		key := p.AppID + "/" + p.Tier
		for _, id := range ids {
			if owner, dup := seen[id]; dup {
				return fmt.Errorf("price_id %s appears in both %s and %s", id, owner, key)
			}
			seen[id] = key
		}
	}
	return nil
}
