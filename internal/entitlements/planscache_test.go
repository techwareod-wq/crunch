package entitlements

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func testPlans() []models.Plan {
	return []models.Plan{
		{
			AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
			Variants: map[string]models.PlanVariant{
				"monthly": {PriceID: "pri_pro_m"},
				"yearly":  {PriceID: "pri_pro_y"},
			},
			Features:      []string{"article.generate", "cms.publish"},
			TrialFeatures: []string{"article.generate"},
		},
		{
			// Inactive: purchasability off, but must still resolve — existing
			// subscribers keep their features.
			AppID: models.AppIDIndexly, Tier: "legacy", Name: "Legacy", Active: false,
			Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_legacy_m"}},
			Features: []string{"article.generate"},
		},
		{
			AppID: "app2", Tier: "starter", Name: "Starter", Active: true,
			Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_app2_m"}},
			Features: []string{"other.feature"},
		},
	}
}

func newTestCache(t *testing.T, plans []models.Plan) *PlansCache {
	t.Helper()
	c := &PlansCache{}
	if err := c.install(plans); err != nil {
		t.Fatalf("install: %v", err)
	}
	return c
}

func TestPlansCacheAccessors(t *testing.T) {
	c := newTestCache(t, testPlans())

	if p, ok := c.ByPriceID("pri_pro_y"); !ok || p.Tier != "pro" {
		t.Errorf("ByPriceID(pri_pro_y) = %v, %v; want pro plan", p, ok)
	}
	if _, ok := c.ByPriceID("pri_unknown"); ok {
		t.Error("unknown price must not resolve")
	}

	// Inactive plans still resolve.
	if p, ok := c.ByPriceID("pri_legacy_m"); !ok || p.Tier != "legacy" {
		t.Errorf("inactive plan must resolve, got %v, %v", p, ok)
	}

	if p, ok := c.ByAppTier(models.AppIDIndexly, "legacy"); !ok || p.Name != "Legacy" {
		t.Errorf("ByAppTier(indexly, legacy) = %v, %v", p, ok)
	}
	if _, ok := c.ByAppTier(models.AppIDIndexly, "nope"); ok {
		t.Error("unknown tier must not resolve")
	}

	// AppForPrice: the authorization stamp — never a fallback.
	if app, ok := c.AppForPrice("pri_app2_m"); !ok || app != "app2" {
		t.Errorf("AppForPrice(pri_app2_m) = %q, %v; want app2", app, ok)
	}
	if _, ok := c.AppForPrice("pri_unknown"); ok {
		t.Error("unmapped price must fail closed")
	}

	ids := c.AppIDs()
	if len(ids) != 2 || ids[0] != "app2" || ids[1] != models.AppIDIndexly {
		t.Errorf("AppIDs() = %v, want [app2 indexly]", ids)
	}
}

func TestPlansCacheEmptyCatalogOK(t *testing.T) {
	// Deploys before the seed run: zero docs must not fail, just resolve nothing.
	c := newTestCache(t, nil)
	if _, ok := c.ByPriceID("pri_x"); ok {
		t.Error("empty cache must resolve nothing")
	}
	if got := c.AppIDs(); len(got) != 0 {
		t.Errorf("empty cache AppIDs = %v, want none", got)
	}
}

func TestPlansCacheDuplicatePriceRejectedKeepsSnapshot(t *testing.T) {
	c := newTestCache(t, testPlans())

	dup := testPlans()
	dup[2].Variants["monthly"] = models.PlanVariant{PriceID: "pri_pro_m"} // collides with indexly/pro

	if err := c.install(dup); err == nil {
		t.Fatal("duplicate price_id across docs must reject the reload")
	}
	// Previous snapshot keeps serving — and still maps the price to indexly.
	if app, ok := c.AppForPrice("pri_pro_m"); !ok || app != models.AppIDIndexly {
		t.Errorf("after rejected reload, AppForPrice(pri_pro_m) = %q, %v; want indexly from old snapshot", app, ok)
	}
	if _, ok := c.ByPriceID("pri_app2_m"); !ok {
		t.Error("old snapshot must remain fully intact after rejected reload")
	}
}

func TestValidatePlanPriceUniqueness(t *testing.T) {
	plans := testPlans()
	if err := models.ValidatePlanPriceUniqueness(plans); err != nil {
		t.Errorf("valid catalog rejected: %v", err)
	}

	plans[0].Variants["yearly"] = models.PlanVariant{PriceID: "pri_app2_m"}
	if err := models.ValidatePlanPriceUniqueness(plans); err == nil {
		t.Error("cross-doc duplicate price_id must be rejected")
	}

	if err := models.ValidatePlanPriceUniqueness([]models.Plan{{AppID: models.AppIDIndexly, Tier: "empty", Active: true}}); err == nil {
		t.Error("ACTIVE plan without any variant price_id must be rejected")
	}
	// A virtual, resolution-only plan (trial_expired) is priceless by design —
	// legal only while inactive.
	if err := models.ValidatePlanPriceUniqueness([]models.Plan{{AppID: models.AppIDIndexly, Tier: TierTrialExpired, Active: false}}); err != nil {
		t.Errorf("inactive priceless plan must be allowed: %v", err)
	}
	if err := models.ValidatePlanPriceUniqueness([]models.Plan{{Tier: "x", Variants: map[string]models.PlanVariant{"monthly": {PriceID: "p"}}}}); err == nil {
		t.Error("plan without app_id must be rejected")
	}
}

func TestIsKnownFeature(t *testing.T) {
	if !IsKnownFeature("article.generate") {
		t.Error("article.generate must be known")
	}
	if IsKnownFeature("article.typo") {
		t.Error("unknown key must not validate")
	}
}
