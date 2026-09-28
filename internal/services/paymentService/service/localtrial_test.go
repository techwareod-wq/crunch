package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

// localTrialSub is a live card-less local trial (source:"trial"), valid.
func localTrialSub(userID primitive.ObjectID) *models.Subscription {
	return &models.Subscription{
		UserID:               userID,
		AppID:                models.AppIDIndexly,
		PaddleSubscriptionID: models.LocalTrialSubscriptionID(userID, models.AppIDIndexly),
		Source:               models.SubSourceTrial,
		Status:               models.SubStatusTrialing,
		ValidTill:            time.Now().UTC().Add(72 * time.Hour),
		WasTrialing:          true,
	}
}

// paddle stays nil in these tests: any Paddle call on a local-trial path is a
// bug and panics on the nil interface.

func TestExtendTrial_LocalTrialSkipsPaddle(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := localTrialSub(userID)
	var wroteTill time.Time
	recomputed := false
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, sub, nil
		},
		setTrialValidTill: func(ctx context.Context, id string, till time.Time) error {
			wroteTill = till
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed = true
			return nil
		},
	}
	s := newTestService(t, m) // paddle nil — must not be called

	got, err := s.ExtendTrial(context.Background(), userID, "", 5)
	if err != nil {
		t.Fatalf("extend local trial: %v", err)
	}
	want := sub.ValidTill.AddDate(0, 0, 5)
	if !wroteTill.Equal(want) || got == nil || !got.Equal(want) {
		t.Errorf("new trial end = %v / wrote %v, want %v", got, wroteTill, want)
	}
	if !recomputed {
		t.Error("expected recompute")
	}
}

func TestEndTrial_LocalTrialConvertRejected(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := localTrialSub(userID)
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, sub, nil
		},
	}
	s := newTestService(t, m)

	if err := s.EndTrial(context.Background(), userID, "", TrialEndModeConvert); !errors.Is(err, ErrTrialNotConvertible) {
		t.Fatalf("err = %v, want ErrTrialNotConvertible", err)
	}
}

func TestEndTrial_LocalTrialCancelClamps(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := localTrialSub(userID)
	canceled := false
	recomputed := false
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, sub, nil
		},
		setCanceled: func(ctx context.Context, id string, at time.Time) error {
			canceled = true
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed = true
			return nil
		},
	}
	s := newTestService(t, m)

	if err := s.EndTrial(context.Background(), userID, "", TrialEndModeCancel); err != nil {
		t.Fatalf("cancel local trial: %v", err)
	}
	if !canceled || !recomputed {
		t.Errorf("canceled=%v recomputed=%v, want both true", canceled, recomputed)
	}
}

func TestCancelSubscription_LocalTrialSchedulesToTrialEnd(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := localTrialSub(userID)
	var scheduledAt *time.Time
	m := &mockStore{
		findLatestByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, sub, nil
		},
		setCanceling: func(ctx context.Context, id string, at *time.Time) error {
			scheduledAt = at
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
	}
	s := newTestService(t, m)

	resp, err := s.CancelSubscription(context.Background(), userID)
	if err != nil {
		t.Fatalf("cancel local trial: %v", err)
	}
	if resp.Status != models.SubStatusCanceling {
		t.Errorf("status = %q, want canceling", resp.Status)
	}
	if scheduledAt == nil || !scheduledAt.Equal(sub.ValidTill) {
		t.Errorf("scheduledCancelAt = %v, want trial end %v", scheduledAt, sub.ValidTill)
	}
}

func TestResubscribe_LocalTrialRestoresTrialing(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := localTrialSub(userID)
	sub.Status = models.SubStatusCanceling
	restored := false
	m := &mockStore{
		findLatestByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, sub, nil
		},
		setTrialing: func(ctx context.Context, id string) error {
			restored = true
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
	}
	s := newTestService(t, m)

	resp, err := s.Resubscribe(context.Background(), userID)
	if err != nil {
		t.Fatalf("resubscribe local trial: %v", err)
	}
	if resp.Mode != dto.ResubscribeModeUncanceled || !restored {
		t.Errorf("mode=%q restored=%v, want uncanceled/true", resp.Mode, restored)
	}
}

// A real paid subscription activating must supersede any live local trial
// BEFORE the projection recompute, so the paid sub wins the newest-valid_till
// rule even if an admin extended the trial past the first billing period.
func TestWebhookSupersede_EndsLocalTrialOnActivation(t *testing.T) {
	userID := primitive.NewObjectID()
	superseded := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m) // arms claims + findSub + a no-op endLocalTrials
	m.endLocalTrials = func(ctx context.Context, uid primitive.ObjectID, appID string, at time.Time) error {
		if uid != userID || appID != models.AppIDIndexly {
			t.Errorf("supersede for uid=%v app=%q, want %v/indexly", uid, appID, userID)
		}
		superseded = true
		return nil
	}
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
	if !superseded {
		t.Error("expected local trial supersede on active subscription")
	}
}
