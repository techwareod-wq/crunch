package service

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
)

// dualVariantCache carries BOTH a Paddle trial price (pri_trial) and a no-trial
// price (pri_notrial) as variants, plus a 7-day card-less trial. This is the
// full checkout config: an eligible first-time user is offered the Paddle trial
// + the card-less trial; once the trial is spent they see only the no-trial price.
func dualVariantCache(t *testing.T) *entitlements.PlansCache {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants: map[string]models.PlanVariant{
			"monthly":       {PriceID: "pri_notrial"},
			"monthly_trial": {PriceID: "pri_trial"},
		},
		Features:      []string{"article.generate", "cms.publish"},
		TrialFeatures: []string{"article.generate"},
		TrialDays:     7,
	}})
	if err != nil {
		t.Fatalf("dual-variant cache: %v", err)
	}
	return cache
}

// noTrialVariantCache carries ONLY the no-trial monthly variant. The Paddle
// trial price is still live in the product but absent from the catalog, so it
// must never be offered — this is the catalog-driven trial gate.
func noTrialVariantCache(t *testing.T) *entitlements.PlansCache {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants: map[string]models.PlanVariant{
			"monthly": {PriceID: "pri_notrial"},
		},
		Features:      []string{"article.generate", "cms.publish"},
		TrialFeatures: []string{"article.generate"},
		TrialDays:     7,
	}})
	if err != nil {
		t.Fatalf("no-trial-variant cache: %v", err)
	}
	return cache
}

// getPlansService wires a service whose Paddle product lists both a trial price
// (TrialInterval set) and a no-trial price. Whether the trial is surfaced is
// decided by the catalog (which variants are mapped) and the user's eligibility.
func getPlansService(m *mockStore, cache *entitlements.PlansCache) *service {
	return &service{
		store: m,
		paddle: &mockPaddle{listActivePrices: func(ctx context.Context, productID string) ([]appdto.PaddlePrice, error) {
			return []appdto.PaddlePrice{
				{ID: "pri_trial", ProductID: "pro_test", TrialInterval: "day", TrialFrequency: 7},
				{ID: "pri_notrial", ProductID: "pro_test"},
			}, nil
		}},
		plans:     cache,
		productID: "pro_test",
	}
}

func eligibleStore(eligible bool) *mockStore {
	return &mockStore{
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
			return eligible, nil
		},
	}
}

// First visit with a trial variant in the catalog: eligible → the Paddle trial
// price (TrialInterval set) AND the card-less trial (cardlessTrialDays set).
func TestGetPlans_TrialCatalogFirstVisitOffersTrial(t *testing.T) {
	s := getPlansService(eligibleStore(true), dualVariantCache(t))

	plans, err := s.GetPlans(context.Background(), primitive.NewObjectID())
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1 (the trial price only)", len(plans))
	}
	p := plans[0]
	if p.PriceID != "pri_trial" {
		t.Errorf("priceId = %q, want pri_trial (Paddle trial offered first visit)", p.PriceID)
	}
	if p.TrialInterval == "" {
		t.Error("expected Paddle trial (trialInterval) on first visit")
	}
	if p.CardlessTrialDays != 7 {
		t.Errorf("cardlessTrialDays = %d, want 7 (card-less trial offered first visit)", p.CardlessTrialDays)
	}
}

// After the trial is spent, a subscription doc exists somewhere in the identity
// → ineligible (also covers the tombstoned same-email account). The user is
// offered ONLY the no-trial price: no Paddle trial, no card-less trial.
func TestGetPlans_TrialCatalogAfterTrialShowsNoTrial(t *testing.T) {
	s := getPlansService(eligibleStore(false), dualVariantCache(t))

	plans, err := s.GetPlans(context.Background(), primitive.NewObjectID())
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1 (the no-trial price only)", len(plans))
	}
	p := plans[0]
	if p.PriceID != "pri_notrial" {
		t.Errorf("priceId = %q, want pri_notrial", p.PriceID)
	}
	if p.TrialInterval != "" {
		t.Error("Paddle trial option must be hidden after the trial is spent")
	}
	if p.CardlessTrialDays != 0 {
		t.Errorf("cardlessTrialDays = %d, want 0 (card-less trial must be hidden)", p.CardlessTrialDays)
	}
}

// Catalog gate: even for an eligible user, if the system catalog has no trial
// variant the Paddle trial price (still live in the product) is never offered.
// The card-less trial is a separate, catalog-driven flow (trial_days), so it is
// unaffected here.
func TestGetPlans_NoTrialVariantHidesPaddleTrialEvenWhenEligible(t *testing.T) {
	s := getPlansService(eligibleStore(true), noTrialVariantCache(t))

	plans, err := s.GetPlans(context.Background(), primitive.NewObjectID())
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1 (only the catalog-mapped no-trial price)", len(plans))
	}
	p := plans[0]
	if p.PriceID != "pri_notrial" {
		t.Errorf("priceId = %q, want pri_notrial (unmapped Paddle trial price filtered out)", p.PriceID)
	}
	if p.TrialInterval != "" {
		t.Error("Paddle trial must not be offered when the catalog has no trial variant")
	}
	if p.CardlessTrialDays != 7 {
		t.Errorf("cardlessTrialDays = %d, want 7 (card-less trial is independent of the Paddle trial variant)", p.CardlessTrialDays)
	}
}
