package entitlements

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

// seedFiles are the per-environment plan catalog seeds (the inputs to
// cmd/entitlementsmigrate -seed-plans). Every environment carries its own
// Paddle price_ids, but the feature-list invariants below must hold in all of
// them, so each guard runs against every file.
var seedFiles = []string{
	"../../values/integration/plans.json",
	"../../values/production/plans.json",
}

// loadSeedPlans reads one environment's plan seed file.
func loadSeedPlans(t *testing.T, path string) []models.Plan {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var plans []models.Plan
	if err := json.Unmarshal(data, &plans); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return plans
}

func planByTier(t *testing.T, plans []models.Plan, tier string) *models.Plan {
	t.Helper()
	for i := range plans {
		if plans[i].AppID == models.AppIDIndexly && plans[i].Tier == tier {
			return &plans[i]
		}
	}
	t.Fatalf("seed has no indexly/%s plan", tier)
	return nil
}

// The trial keeps every capability except manual keywords and AI assist
// (locked decision 8). Deriving trial_features from features here guards
// against a future feature key being added to the paid list but silently
// omitted from the trial list — which would hide a whole surface from trial
// users with no code review signal.
func TestSeedTrialFeaturesOmitOnlyManualAndAIAssist(t *testing.T) {
	for _, path := range seedFiles {
		t.Run(path, func(t *testing.T) {
			testSeedTrialFeatures(t, path)
		})
	}
}

func testSeedTrialFeatures(t *testing.T, path string) {
	pro := planByTier(t, loadSeedPlans(t, path), "pro")

	trialSet := map[string]bool{}
	for _, f := range pro.TrialFeatures {
		trialSet[f] = true
	}
	blockedDuringTrial := map[string]bool{
		string(FeatureKeywordManual):   true,
		string(FeatureArticleAIAssist): true,
	}

	for _, f := range pro.Features {
		if blockedDuringTrial[f] {
			if trialSet[f] {
				t.Errorf("feature %q must NOT be in trial_features", f)
			}
			continue
		}
		if !trialSet[f] {
			t.Errorf("feature %q missing from trial_features — trials keep every key except keyword.manual and article.ai_assist", f)
		}
	}
	for _, f := range pro.TrialFeatures {
		if blockedDuringTrial[f] {
			t.Errorf("blocked feature %q leaked into trial_features", f)
		}
	}

	// Every seeded key must be a defined constant — a typo'd key resolves to
	// nothing and silently 402s the surface it was meant to open.
	for _, f := range append(append([]string{}, pro.Features...), pro.TrialFeatures...) {
		if !IsKnownFeature(f) {
			t.Errorf("%s seeds unknown feature key %q", path, f)
		}
	}
}

// The team plan carries EXACTLY Pro's feature list (locked decision 3: a seat
// grants full indexly access) and no trial machinery (decision 2). It ships
// inactive with no price until the Paddle team product exists — while
// priceless it MUST stay inactive or the seed validation rejects the file.
func TestSeedTeamPlanMirrorsPro(t *testing.T) {
	for _, path := range seedFiles {
		t.Run(path, func(t *testing.T) {
			plans := loadSeedPlans(t, path)
			pro := planByTier(t, plans, "pro")
			team := planByTier(t, plans, TierTeam)

			proSet := map[string]bool{}
			for _, f := range pro.Features {
				proSet[f] = true
			}
			if len(team.Features) != len(pro.Features) {
				t.Errorf("team features = %v, want exactly Pro's list %v", team.Features, pro.Features)
			}
			for _, f := range team.Features {
				if !proSet[f] {
					t.Errorf("team feature %q is not in Pro's list", f)
				}
			}
			if len(team.TrialFeatures) != 0 || team.TrialDays != 0 {
				t.Error("team plan must carry no trial (locked decision 2)")
			}
			if len(team.PriceIDs()) == 0 && team.Active {
				t.Error("priceless team plan must stay inactive until the Paddle price IDs are filled")
			}
			if err := models.ValidatePlanPriceUniqueness(plans); err != nil {
				t.Errorf("%s fails seed validation: %v", path, err)
			}
		})
	}
}

// The trial_expired virtual plan: view-only keys, inactive, priceless — and
// it must pass the same validation the seed run applies.
func TestSeedTrialExpiredPlan(t *testing.T) {
	for _, path := range seedFiles {
		t.Run(path, func(t *testing.T) {
			testSeedTrialExpiredPlan(t, path)
		})
	}
}

func testSeedTrialExpiredPlan(t *testing.T, path string) {
	plans := loadSeedPlans(t, path)
	te := planByTier(t, plans, TierTrialExpired)

	if te.Active {
		t.Error("trial_expired must be inactive (resolution-only, never purchasable)")
	}
	if len(te.PriceIDs()) != 0 {
		t.Errorf("trial_expired must have no prices, got %v", te.PriceIDs())
	}
	want := map[string]bool{
		string(FeatureKeywordView):   true,
		string(FeatureArticleView):   true,
		string(FeatureScheduleView):  true,
		string(FeatureAnalyticsView): true, // free on every plan (analytics D11), a view key
	}
	if len(te.Features) != len(want) {
		t.Fatalf("trial_expired features = %v, want exactly the view keys", te.Features)
	}
	for _, f := range te.Features {
		if !want[f] {
			t.Errorf("trial_expired must hold only view keys, got %q", f)
		}
	}

	if err := models.ValidatePlanPriceUniqueness(plans); err != nil {
		t.Errorf("%s fails seed validation: %v", path, err)
	}
}
