package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
)

// buildFeatureGuardedRoute assembles a route the way production code does —
// handler first, then .WithFeature().WithJWTAuthentication() ordering is
// emulated by injecting the user into context before the guard runs. An empty
// feature is the subscription-only (app-level) gate.
func buildFeatureGuardedRoute(t *testing.T, path string, feature entitlements.Feature) http.Handler {
	t.Helper()
	Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached handler"))
	})).WithFeature(models.AppIDIndexly, feature)
	return routes[path]
}

func requestWithUser(user *models.User) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return r.WithContext(context.WithValue(r.Context(), UserContextKey, user))
}

func entitledUser(status string, validTill time.Time) *models.User {
	return &models.User{
		ID: primitive.NewObjectID(),
		Entitlements: map[string]models.AppEntitlement{
			models.AppIDIndexly: {Status: status, PriceID: "pri_test", ValidTill: validTill, Ver: 1},
		},
	}
}

func withTestPlansCache(t *testing.T) {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants:      map[string]models.PlanVariant{"monthly": {PriceID: "pri_test"}},
		Features:      []string{"article.generate", "cms.publish"},
		TrialFeatures: []string{"article.generate"},
	}})
	if err != nil {
		t.Fatalf("test plans cache: %v", err)
	}
	orig := plansCache
	SetPlansCache(cache)
	t.Cleanup(func() { plansCache = orig })
}

// The empty-feature gate is the subscription-only check (parity with the old
// WithAppSubscription). It returns Allow before touching the plans cache.

func TestWithFeature_EmptyFeature_ValidProjectionPassesThrough(t *testing.T) {
	h := buildFeatureGuardedRoute(t, "/test/app-sub-valid", "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(entitledUser(models.SubStatusActive, time.Now().Add(24*time.Hour))))

	if rec.Code != http.StatusOK {
		t.Fatalf("valid projection: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestWithFeature_EmptyFeature_ExpiredProjectionGets402WithCode(t *testing.T) {
	h := buildFeatureGuardedRoute(t, "/test/app-sub-expired", "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(entitledUser(models.SubStatusActive, time.Now().Add(-time.Hour))))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expired projection: status = %d, want 402", rec.Code)
	}
	// The frontend's paywall redirect branches on this code.
	if !strings.Contains(rec.Body.String(), `"code":"subscription_required"`) {
		t.Errorf("402 must carry code subscription_required on the wire, got: %s", rec.Body.String())
	}
}

func TestWithFeature_EmptyFeature_NoEntitlementGets402(t *testing.T) {
	h := buildFeatureGuardedRoute(t, "/test/app-sub-none", "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(&models.User{ID: primitive.NewObjectID()}))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("no entitlement: status = %d, want 402", rec.Code)
	}
}

func TestWithFeature_MissingUserIsWiringBug500(t *testing.T) {
	// No user in context means the guard ran before JWT auth — a chain-order
	// bug that must surface as a 500, not masquerade as 401/402.
	h := buildFeatureGuardedRoute(t, "/test/app-sub-no-user", "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing user: status = %d, want 500", rec.Code)
	}
}

func TestWithFeature_IncludedFeatureAllows(t *testing.T) {
	withTestPlansCache(t)
	h := buildFeatureGuardedRoute(t, "/test/feature-allowed", entitlements.FeatureCMSPublish)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(entitledUser(models.SubStatusActive, time.Now().Add(24*time.Hour))))

	if rec.Code != http.StatusOK {
		t.Fatalf("included feature: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestWithFeature_ExcludedFeatureGets402WithData(t *testing.T) {
	withTestPlansCache(t)
	h := buildFeatureGuardedRoute(t, "/test/feature-excluded", entitlements.FeatureCMSPublish)

	// Trialing: the reduced trial_features list doesn't include cms.publish.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(entitledUser(models.SubStatusTrialing, time.Now().Add(24*time.Hour))))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("excluded feature: status = %d, want 402", rec.Code)
	}
	body := rec.Body.String()
	for _, fragment := range []string{`"code":"feature_not_included"`, `"app":"indexly"`, `"feature":"cms.publish"`} {
		if !strings.Contains(body, fragment) {
			t.Errorf("402 envelope missing %s: %s", fragment, body)
		}
	}
}

func TestWithFeature_NoAccessIsSubscriptionRequiredNotFeature(t *testing.T) {
	withTestPlansCache(t)
	h := buildFeatureGuardedRoute(t, "/test/feature-no-sub", entitlements.FeatureCMSPublish)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithUser(&models.User{ID: primitive.NewObjectID()}))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("no subscription: status = %d, want 402", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"subscription_required"`) {
		t.Errorf("lapsed user must get the paywall code, not feature_not_included: %s", rec.Body.String())
	}
}
