package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestForUserOmitsAdminOnlyFields is the leakage guard: the client-facing
// projection must never carry priceId, per-app tier, or the effective feature
// list. If someone re-adds any of those to the lean structs, this fails.
func TestForUserOmitsAdminOnlyFields(t *testing.T) {
	validTill := time.Date(2026, 7, 20, 5, 32, 14, 0, time.UTC)
	full := &PaymentStatusResponse{
		HasSubscription: true,
		Subscription: &SubscriptionDTO{
			Status:    "trialing",
			PriceID:   "pri_01ksz7m6kx81r5fjxsm6wcsvws",
			ValidTill: validTill,
			IsValid:   true,
		},
		Apps: map[string]AppAccessDTO{
			"indexly": {
				Status:    "trialing",
				Tier:      "pro",
				ValidTill: &validTill,
				Features:  []string{"article.edit", "article.generate"},
			},
		},
	}

	blob, err := json.Marshal(full.ForUser())
	if err != nil {
		t.Fatalf("marshal ForUser: %v", err)
	}
	body := string(blob)

	for _, leaked := range []string{"priceId", "tier", "features", "pri_01ksz7m6kx81r5fjxsm6wcsvws", "article.edit"} {
		if strings.Contains(body, leaked) {
			t.Errorf("client status leaks %q: %s", leaked, body)
		}
	}
}

// TestForUserPreservesClientFields keeps the fields the app actually reads.
func TestForUserPreservesClientFields(t *testing.T) {
	validTill := time.Date(2026, 7, 20, 5, 32, 14, 0, time.UTC)
	cancelAt := validTill.Add(-time.Hour)
	full := &PaymentStatusResponse{
		HasSubscription: true,
		Subscription: &SubscriptionDTO{
			Status:            "canceling",
			PriceID:           "pri_x",
			ValidTill:         validTill,
			ScheduledCancelAt: &cancelAt,
			IsValid:           true,
			IsLocalTrial:      true,
		},
		Apps: map[string]AppAccessDTO{
			"indexly": {Status: "trialing", Tier: "pro", ValidTill: &validTill, Features: []string{"x"}},
		},
	}

	got := full.ForUser()

	if !got.HasSubscription {
		t.Error("hasSubscription not preserved")
	}
	if got.Subscription == nil {
		t.Fatal("subscription dropped")
	}
	if got.Subscription.Status != "canceling" || !got.Subscription.IsValid ||
		!got.Subscription.ValidTill.Equal(validTill) ||
		got.Subscription.ScheduledCancelAt == nil || !got.Subscription.ScheduledCancelAt.Equal(cancelAt) ||
		!got.Subscription.IsLocalTrial {
		t.Errorf("subscription fields not preserved: %+v", got.Subscription)
	}
	app, ok := got.Apps["indexly"]
	if !ok {
		t.Fatal("app entry dropped")
	}
	if app.Status != "trialing" || app.ValidTill == nil || !app.ValidTill.Equal(validTill) {
		t.Errorf("app fields not preserved: %+v", app)
	}
}

// TestForUserNilSubscription: no subscription means a nil pointer (omitted in
// JSON), not a zero-value object.
func TestForUserNilSubscription(t *testing.T) {
	full := &PaymentStatusResponse{Apps: map[string]AppAccessDTO{}}
	got := full.ForUser()
	if got.Subscription != nil {
		t.Errorf("expected nil subscription, got %+v", got.Subscription)
	}
	if got.Apps == nil {
		t.Error("apps map should be non-nil even when empty")
	}
}
