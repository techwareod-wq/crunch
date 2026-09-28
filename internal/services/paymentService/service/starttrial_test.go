package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/store"
)

// trialPlansCache maps pri_trial → an indexly plan offering a 7-day card-less
// trial, and pri_notrial → the same app with no trial.
func trialPlansCache(t *testing.T) *entitlements.PlansCache {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{
		{
			AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
			Variants:      map[string]models.PlanVariant{"monthly": {PriceID: "pri_trial"}},
			Features:      []string{"article.generate", "cms.publish"},
			TrialFeatures: []string{"article.generate"},
			TrialDays:     7,
		},
		{
			AppID: models.AppIDIndexly, Tier: "basic", Name: "Basic", Active: true,
			Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_notrial"}},
			Features: []string{"article.generate"},
		},
	})
	if err != nil {
		t.Fatalf("trial plans cache: %v", err)
	}
	return cache
}

func newTrialTestService(t *testing.T, st store.Store) *service {
	return &service{store: st, paddle: nil, plans: trialPlansCache(t), dispatcher: &mockDispatcher{}, productID: "pro_test"}
}

func TestStartTrial_WritesTrialingSubAndRecomputes(t *testing.T) {
	userID := primitive.NewObjectID()
	var inserted *models.Subscription
	recomputed := false
	m := &mockStore{
		webEntityFinalised: func(ctx context.Context, uid primitive.ObjectID) (bool, error) {
			return true, nil
		},
		findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
			return false, nil, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
			return true, nil
		},
		insertTrialSub: func(ctx context.Context, sub *models.Subscription) error {
			inserted = sub
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed = true
			return nil
		},
	}
	s := newTrialTestService(t, m)

	resp, err := s.StartTrial(context.Background(), userID, "pri_trial")
	if err != nil {
		t.Fatalf("StartTrial: %v", err)
	}
	if inserted == nil {
		t.Fatal("no subscription inserted")
	}
	if inserted.Source != models.SubSourceTrial {
		t.Errorf("source = %q, want %q", inserted.Source, models.SubSourceTrial)
	}
	if inserted.Status != models.SubStatusTrialing || !inserted.WasTrialing {
		t.Errorf("status/wasTrialing = %q/%v, want trialing/true", inserted.Status, inserted.WasTrialing)
	}
	if inserted.PaddleSubscriptionID != models.LocalTrialSubscriptionID(userID, models.AppIDIndexly) {
		t.Errorf("unexpected synthetic id %q", inserted.PaddleSubscriptionID)
	}
	// valid_till ≈ the never-lapses horizon — the free plan has no date
	// expiry; only the lifetime article cap gates generation.
	wantEnd := models.LocalTrialValidTill(time.Now().UTC())
	if diff := inserted.ValidTill.Sub(wantEnd); diff > time.Minute || diff < -time.Minute {
		t.Errorf("valid_till = %v, want ~%v", inserted.ValidTill, wantEnd)
	}
	if !recomputed {
		t.Error("expected recompute after local write")
	}
	if resp.Status != models.SubStatusTrialing || resp.AppID != models.AppIDIndexly {
		t.Errorf("resp = %+v, want trialing/indexly", resp)
	}
}

func TestStartTrial_UnknownPrice(t *testing.T) {
	m := &mockStore{}
	s := newTrialTestService(t, m)
	_, err := s.StartTrial(context.Background(), primitive.NewObjectID(), "pri_missing")
	if !errors.Is(err, ErrTrialNotAvailable) {
		t.Fatalf("err = %v, want ErrTrialNotAvailable", err)
	}
}

func TestStartTrial_PlanWithoutTrialDays(t *testing.T) {
	m := &mockStore{}
	s := newTrialTestService(t, m)
	_, err := s.StartTrial(context.Background(), primitive.NewObjectID(), "pri_notrial")
	if !errors.Is(err, ErrTrialNotAvailable) {
		t.Fatalf("err = %v, want ErrTrialNotAvailable", err)
	}
}

func TestStartTrial_IneligibleIdentity(t *testing.T) {
	userID := primitive.NewObjectID()
	m := &mockStore{
		webEntityFinalised: func(ctx context.Context, uid primitive.ObjectID) (bool, error) {
			return true, nil
		},
		findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
			return false, nil, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
			return false, nil // prior subscription history
		},
	}
	s := newTrialTestService(t, m)
	_, err := s.StartTrial(context.Background(), userID, "pri_trial")
	if !errors.Is(err, ErrTrialAlreadyUsed) {
		t.Fatalf("err = %v, want ErrTrialAlreadyUsed", err)
	}
}

func TestStartTrial_OnboardingIncomplete(t *testing.T) {
	userID := primitive.NewObjectID()
	m := &mockStore{
		webEntityFinalised: func(ctx context.Context, uid primitive.ObjectID) (bool, error) {
			return false, nil // no finalised web entity
		},
	}
	s := newTrialTestService(t, m)
	_, err := s.StartTrial(context.Background(), userID, "pri_trial")
	if !errors.Is(err, ErrOnboardingIncomplete) {
		t.Fatalf("err = %v, want ErrOnboardingIncomplete", err)
	}
}

func TestStartTrial_DuplicateLiveTrialIsIdempotent(t *testing.T) {
	userID := primitive.NewObjectID()
	live := &models.Subscription{
		PaddleSubscriptionID: models.LocalTrialSubscriptionID(userID, models.AppIDIndexly),
		AppID:                models.AppIDIndexly,
		Source:               models.SubSourceTrial,
		Status:               models.SubStatusTrialing,
		ValidTill:            time.Now().UTC().Add(48 * time.Hour),
	}
	m := &mockStore{
		webEntityFinalised: func(ctx context.Context, uid primitive.ObjectID) (bool, error) {
			return true, nil
		},
		findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
			return false, nil, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
			return true, nil
		},
		insertTrialSub: func(ctx context.Context, sub *models.Subscription) error {
			return mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, live, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
	}
	s := newTrialTestService(t, m)
	resp, err := s.StartTrial(context.Background(), userID, "pri_trial")
	if err != nil {
		t.Fatalf("duplicate live trial should be idempotent, got: %v", err)
	}
	if !resp.ValidTill.Equal(live.ValidTill) {
		t.Errorf("resp.ValidTill = %v, want existing %v", resp.ValidTill, live.ValidTill)
	}
}

func TestStartTrial_DuplicateLapsedTrialIsSpent(t *testing.T) {
	userID := primitive.NewObjectID()
	lapsed := &models.Subscription{
		PaddleSubscriptionID: models.LocalTrialSubscriptionID(userID, models.AppIDIndexly),
		AppID:                models.AppIDIndexly,
		Source:               models.SubSourceTrial,
		Status:               models.SubStatusTrialing,
		ValidTill:            time.Now().UTC().Add(-1 * time.Hour), // already expired
	}
	m := &mockStore{
		webEntityFinalised: func(ctx context.Context, uid primitive.ObjectID) (bool, error) {
			return true, nil
		},
		findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
			return false, nil, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
			return true, nil
		},
		insertTrialSub: func(ctx context.Context, sub *models.Subscription) error {
			return mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, lapsed, nil
		},
	}
	s := newTrialTestService(t, m)
	_, err := s.StartTrial(context.Background(), userID, "pri_trial")
	if !errors.Is(err, ErrTrialAlreadyUsed) {
		t.Fatalf("err = %v, want ErrTrialAlreadyUsed", err)
	}
}
