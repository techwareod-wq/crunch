package entitlements

import (
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

var resolverNow = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

func resolverCache(t *testing.T) *PlansCache {
	t.Helper()
	c, err := NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants:      map[string]models.PlanVariant{"monthly": {PriceID: "pri_pro"}},
		Features:      []string{"article.generate", "cms.publish"},
		TrialFeatures: []string{"article.generate"},
	}})
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	return c
}

func userWith(ent models.AppEntitlement) *models.User {
	return &models.User{Entitlements: map[string]models.AppEntitlement{models.AppIDIndexly: ent}}
}

func TestResolve(t *testing.T) {
	future := resolverNow.Add(24 * time.Hour)
	past := resolverNow.Add(-24 * time.Hour)
	paid := models.AppEntitlement{Status: models.SubStatusActive, PriceID: "pri_pro", ValidTill: future}
	trialing := models.AppEntitlement{Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: future}

	grant := func(key string, exp *time.Time) models.OverrideEntry {
		return models.OverrideEntry{Key: key, ExpiresAt: exp, By: "admin@x.com", At: past}
	}

	cases := []struct {
		name    string
		user    *models.User
		feature Feature
		want    Decision
	}{
		{"nil user", nil, "", NoSubscription},
		{"no entitlement entry", &models.User{}, "", NoSubscription},
		{"expired sub, app gate", userWith(models.AppEntitlement{Status: models.SubStatusActive, PriceID: "pri_pro", ValidTill: past}), "", NoSubscription},
		{"valid sub, app gate", userWith(paid), "", Allow},
		{"canceling still valid keeps access", userWith(models.AppEntitlement{Status: models.SubStatusCanceling, PriceID: "pri_pro", ValidTill: future}), "", Allow},

		{"paid list includes paid feature", userWith(paid), FeatureCMSPublish, Allow},
		{"trialing gets reduced list", userWith(trialing), FeatureCMSPublish, FeatureNotIncluded},
		{"trialing keeps trial feature", userWith(trialing), FeatureArticleGenerate, Allow},

		{"grant adds", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: future,
			Grants: []models.OverrideEntry{grant("cms.publish", nil)},
		}), FeatureCMSPublish, Allow},
		{"revoke removes", userWith(models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_pro", ValidTill: future,
			Revokes: []models.OverrideEntry{grant("cms.publish", nil)},
		}), FeatureCMSPublish, FeatureNotIncluded},
		{"revoke of granted wins", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: future,
			Grants:  []models.OverrideEntry{grant("cms.publish", nil)},
			Revokes: []models.OverrideEntry{grant("cms.publish", nil)},
		}), FeatureCMSPublish, FeatureNotIncluded},
		{"expired grant ignored", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: future,
			Grants: []models.OverrideEntry{grant("cms.publish", &past)},
		}), FeatureCMSPublish, FeatureNotIncluded},
		{"expired revoke ignored", userWith(models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_pro", ValidTill: future,
			Revokes: []models.OverrideEntry{grant("cms.publish", &past)},
		}), FeatureCMSPublish, Allow},

		{"unknown price fails closed on features", userWith(models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_gone", ValidTill: future,
		}), FeatureArticleGenerate, FeatureNotIncluded},
		{"unknown price still allows app gate", userWith(models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_gone", ValidTill: future,
		}), "", Allow},
		{"unknown price: grants still apply", userWith(models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_gone", ValidTill: future,
			Grants: []models.OverrideEntry{grant("article.generate", nil)},
		}), FeatureArticleGenerate, Allow},
	}

	cache := resolverCache(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.user, models.AppIDIndexly, tc.feature, cache, resolverNow); got != tc.want {
				t.Errorf("Resolve() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveNilCacheFailsClosedOnFeaturesOnly(t *testing.T) {
	future := resolverNow.Add(24 * time.Hour)
	u := userWith(models.AppEntitlement{Status: models.SubStatusActive, PriceID: "pri_pro", ValidTill: future})

	// App-level gate never touches the cache — parity doesn't depend on plan data.
	if got := Resolve(u, models.AppIDIndexly, "", nil, resolverNow); got != Allow {
		t.Errorf("app gate with nil cache = %v, want Allow", got)
	}
	if got := Resolve(u, models.AppIDIndexly, FeatureArticleGenerate, nil, resolverNow); got != FeatureNotIncluded {
		t.Errorf("feature gate with nil cache = %v, want FeatureNotIncluded (fail closed)", got)
	}
}

// expiredTrialCache is resolverCache plus the trial_expired virtual plan
// (priceless, inactive) the resolver falls back to for expired local trials.
func expiredTrialCache(t *testing.T) *PlansCache {
	t.Helper()
	c, err := NewPlansCacheFromPlans([]models.Plan{
		{
			AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
			Variants:      map[string]models.PlanVariant{"monthly": {PriceID: "pri_pro"}},
			Features:      []string{"article.generate", "cms.publish", "keyword.view", "article.view", "schedule.view"},
			TrialFeatures: []string{"article.generate", "keyword.view", "article.view", "schedule.view"},
		},
		{
			AppID: models.AppIDIndexly, Tier: TierTrialExpired, Name: "Trial expired", Active: false,
			Features:      []string{"keyword.view", "article.view", "schedule.view"},
			TrialFeatures: []string{"keyword.view", "article.view", "schedule.view"},
		},
	})
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	return c
}

func TestResolveExpiredTrial(t *testing.T) {
	past := resolverNow.Add(-24 * time.Hour)
	expiredTrial := models.AppEntitlement{Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: past}
	expiredPaid := models.AppEntitlement{Status: models.SubStatusCanceled, PriceID: "pri_pro", ValidTill: past}

	cache := expiredTrialCache(t)

	cases := []struct {
		name    string
		user    *models.User
		feature Feature
		want    Decision
	}{
		// The derivation: an expired trialing entitlement resolves the
		// trial_expired plan's view-only set — browse, never act.
		{"expired trial: keyword view allowed", userWith(expiredTrial), FeatureKeywordView, Allow},
		{"expired trial: article view allowed", userWith(expiredTrial), FeatureArticleView, Allow},
		{"expired trial: schedule view allowed", userWith(expiredTrial), FeatureScheduleView, Allow},
		{"expired trial: write feature denied", userWith(expiredTrial), FeatureArticleGenerate, FeatureNotIncluded},
		{"expired trial: manual keyword denied", userWith(expiredTrial), FeatureKeywordManual, FeatureNotIncluded},
		// The bare app gate (subscription-only routes) still denies.
		{"expired trial: bare app gate denies", userWith(expiredTrial), "", NoSubscription},
		// A lapsed PAID sub now resolves the SAME view-only set: read access to
		// everything it produced, every write 402s, bare app gate still denies.
		{"expired paid: keyword view allowed", userWith(expiredPaid), FeatureKeywordView, Allow},
		{"expired paid: article view allowed", userWith(expiredPaid), FeatureArticleView, Allow},
		{"expired paid: schedule view allowed", userWith(expiredPaid), FeatureScheduleView, Allow},
		{"expired paid: write feature denied", userWith(expiredPaid), FeatureArticleGenerate, FeatureNotIncluded},
		{"expired paid: manual keyword denied", userWith(expiredPaid), FeatureKeywordManual, FeatureNotIncluded},
		{"expired paid: bare app gate denies", userWith(expiredPaid), "", NoSubscription},
		// Overrides still apply on the derived set.
		{"expired trial: grant adds write feature", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: past,
			Grants: []models.OverrideEntry{{Key: "article.generate", By: "admin@x.com", At: past}},
		}), FeatureArticleGenerate, Allow},
		{"expired trial: revoke removes view feature", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: past,
			Revokes: []models.OverrideEntry{{Key: "keyword.view", By: "admin@x.com", At: past}},
		}), FeatureKeywordView, FeatureNotIncluded},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.user, models.AppIDIndexly, tc.feature, cache, resolverNow); got != tc.want {
				t.Errorf("Resolve() = %v, want %v", got, tc.want)
			}
		})
	}

	// Missing trial_expired plan doc fails closed to grants-only.
	bare := resolverCache(t)
	if got := Resolve(userWith(expiredTrial), models.AppIDIndexly, FeatureKeywordView, bare, resolverNow); got != FeatureNotIncluded {
		t.Errorf("expired trial without trial_expired plan doc = %v, want FeatureNotIncluded (fail closed)", got)
	}
}

// Comp (LinkedIn billing plan §3.7): with no valid subscription behind the
// entitlement, an active admin comp grants access as-if-subscribed to the
// app's active paid tier — app-level Allow, feature checks against that
// plan's full feature list, overrides still applied. An expired comp changes
// nothing.
func TestResolveComp(t *testing.T) {
	past := resolverNow.Add(-24 * time.Hour)
	future := resolverNow.Add(24 * time.Hour)

	activeComp := &models.CompGrant{GrantedBy: "admin@x.com", GrantedAt: past}
	timedComp := &models.CompGrant{GrantedBy: "admin@x.com", GrantedAt: past, ValidTill: &future}
	expiredComp := &models.CompGrant{GrantedBy: "admin@x.com", GrantedAt: past, ValidTill: &past}

	cases := []struct {
		name    string
		user    *models.User
		feature Feature
		want    Decision
	}{
		// No subscription history at all, comp only.
		{"comp: bare app gate allows", userWith(models.AppEntitlement{Comp: activeComp}), "", Allow},
		{"comp: paid feature allows", userWith(models.AppEntitlement{Comp: activeComp}), FeatureCMSPublish, Allow},
		{"comp with future expiry allows", userWith(models.AppEntitlement{Comp: timedComp}), "", Allow},
		{"expired comp denies app gate", userWith(models.AppEntitlement{Comp: expiredComp}), "", NoSubscription},
		// Comp over a LAPSED subscription (the cutover case: existing user
		// whose access would otherwise 402).
		{"comp over lapsed sub: app gate allows", userWith(models.AppEntitlement{
			Status: models.SubStatusCanceled, PriceID: "pri_pro", ValidTill: past, Comp: activeComp,
		}), "", Allow},
		{"comp over lapsed sub: paid feature allows", userWith(models.AppEntitlement{
			Status: models.SubStatusCanceled, PriceID: "pri_pro", ValidTill: past, Comp: activeComp,
		}), FeatureCMSPublish, Allow},
		// Overrides still apply on the comp set.
		{"comp: revoke removes", userWith(models.AppEntitlement{
			Comp:    activeComp,
			Revokes: []models.OverrideEntry{{Key: "cms.publish", By: "admin@x.com", At: past}},
		}), FeatureCMSPublish, FeatureNotIncluded},
		// A valid subscription short-circuits before comp is consulted.
		{"valid sub with comp still resolves the sub's set", userWith(models.AppEntitlement{
			Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: future, Comp: activeComp,
		}), FeatureCMSPublish, FeatureNotIncluded}, // trial list, not comp's paid list
	}

	cache := resolverCache(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.user, models.AppIDIndexly, tc.feature, cache, resolverNow); got != tc.want {
				t.Errorf("Resolve() = %v, want %v", got, tc.want)
			}
		})
	}

	// EffectiveFeatures on a comped, sub-less entitlement reports the active
	// paid plan's full list (status surface renders the real feature set).
	got := EffectiveFeatures(userWith(models.AppEntitlement{Comp: activeComp}), models.AppIDIndexly, cache, resolverNow)
	want := []string{"article.generate", "cms.publish"}
	if len(got) != len(want) {
		t.Fatalf("comp EffectiveFeatures = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("comp EffectiveFeatures = %v, want %v", got, want)
		}
	}
}

func TestEffectiveFeaturesExpiredTrial(t *testing.T) {
	past := resolverNow.Add(-24 * time.Hour)
	cache := expiredTrialCache(t)

	got := EffectiveFeatures(userWith(models.AppEntitlement{
		Status: models.SubStatusTrialing, PriceID: "pri_pro", ValidTill: past,
	}), models.AppIDIndexly, cache, resolverNow)
	want := []string{"article.view", "keyword.view", "schedule.view"}
	if len(got) != len(want) {
		t.Fatalf("EffectiveFeatures = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EffectiveFeatures = %v, want %v", got, want)
		}
	}

	// A lapsed paid sub reports the same view-only set, so the frontend can
	// render its read-only state identically to a lapsed trial.
	gotPaid := EffectiveFeatures(userWith(models.AppEntitlement{
		Status: models.SubStatusCanceled, PriceID: "pri_pro", ValidTill: past,
	}), models.AppIDIndexly, cache, resolverNow)
	if len(gotPaid) != len(want) {
		t.Fatalf("expired paid EffectiveFeatures = %v, want %v", gotPaid, want)
	}
	for i := range want {
		if gotPaid[i] != want[i] {
			t.Fatalf("expired paid EffectiveFeatures = %v, want %v", gotPaid, want)
		}
	}
}
