package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
)

// monthlyOnlyCache is one indexly plan carrying a single monthly price. Used to
// prove offeredPrices filters everything not in the catalog.
func monthlyOnlyCache(t *testing.T) *entitlements.PlansCache {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants: map[string]models.PlanVariant{
			"monthly": {PriceID: "pri_monthly"},
		},
		Features:      []string{"article.generate", "cms.publish"},
		TrialFeatures: []string{"article.generate"},
		TrialDays:     7,
	}})
	if err != nil {
		t.Fatalf("monthly-only cache: %v", err)
	}
	return cache
}

// offeredPrices returns only catalog-mapped prices: a price live in Paddle but
// absent from plans.json (e.g. a retired trial price) is never offered, so the
// overlay can't open against a price the webhook would reject as unmapped.
func TestOfferedPrices(t *testing.T) {
	monthly := appdto.PaddlePrice{ID: "pri_monthly"}
	retiredTrial := appdto.PaddlePrice{ID: "pri_retired_trial", TrialInterval: "day", TrialFrequency: 7}
	unknown := appdto.PaddlePrice{ID: "pri_unknown"}

	svc := &service{productID: "pro_test", plans: monthlyOnlyCache(t)}

	got := svc.offeredPrices([]appdto.PaddlePrice{monthly, retiredTrial, unknown})
	if len(got) != 1 || got[0].ID != "pri_monthly" {
		ids := make([]string, 0, len(got))
		for _, p := range got {
			ids = append(ids, p.ID)
		}
		t.Fatalf("offeredPrices = %v, want [pri_monthly] (only the catalog-mapped price)", ids)
	}
}

// selectPricesForUser splits the (already catalog-filtered) prices into trial
// vs no-trial by eligibility, with safe fallbacks when a bucket is empty.
func TestSelectPricesForUser(t *testing.T) {
	trial := appdto.PaddlePrice{ID: "pri_trial", TrialInterval: "day", TrialFrequency: 7}
	noTrial := appdto.PaddlePrice{ID: "pri_notrial"}

	tests := []struct {
		name     string
		prices   []appdto.PaddlePrice
		eligible bool
		wantIDs  []string
	}{
		{
			name:     "eligible user gets the trial price",
			prices:   []appdto.PaddlePrice{noTrial, trial},
			eligible: true,
			wantIDs:  []string{"pri_trial"},
		},
		{
			name:     "returning user gets the no-trial price",
			prices:   []appdto.PaddlePrice{noTrial, trial},
			eligible: false,
			wantIDs:  []string{"pri_notrial"},
		},
		{
			name:     "eligible user falls back to no-trial when the catalog has no trial price",
			prices:   []appdto.PaddlePrice{noTrial},
			eligible: true,
			wantIDs:  []string{"pri_notrial"},
		},
		{
			name:     "returning user falls back to trial when no no-trial price exists",
			prices:   []appdto.PaddlePrice{trial},
			eligible: false,
			wantIDs:  []string{"pri_trial"},
		},
		{
			name:     "no prices yields nothing",
			prices:   nil,
			eligible: true,
			wantIDs:  nil,
		},
	}

	svc := &service{productID: "pro_test"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.selectPricesForUser(tt.prices, tt.eligible, primitive.NewObjectID())
			gotIDs := make([]string, 0, len(got))
			for _, p := range got {
				gotIDs = append(gotIDs, p.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}
