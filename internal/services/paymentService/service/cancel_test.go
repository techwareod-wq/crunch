package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

// Cancel-all must terminate every non-canceled doc: the Paddle sub via the
// API, the local trial via the authoritative Mongo write, and skip the
// already-canceled doc entirely (that skip is what makes retries converge).
func TestCancelAllSubscriptionsImmediately_MixedSet(t *testing.T) {
	userID := primitive.NewObjectID()
	trialID := models.LocalTrialSubscriptionID(userID, models.AppIDIndexly)

	var paddleCancels, clamps []string
	recomputes := 0
	m := &mockStore{
		findSubsByUser: func(ctx context.Context, uid primitive.ObjectID) ([]models.Subscription, error) {
			return []models.Subscription{
				{UserID: uid, PaddleSubscriptionID: "sub_old", Status: models.SubStatusCanceled},
				{UserID: uid, PaddleSubscriptionID: "sub_active", Status: models.SubStatusActive, AppID: models.AppIDIndexly},
				{UserID: uid, PaddleSubscriptionID: trialID, Status: models.SubStatusTrialing, Source: models.SubSourceTrial, AppID: models.AppIDIndexly},
			}, nil
		},
		setCanceled: func(ctx context.Context, id string, at time.Time) error {
			clamps = append(clamps, id)
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputes++
			return nil
		},
	}
	s := newTestService(t, m)
	s.paddle = &mockPaddle{
		cancelImmediately: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
			paddleCancels = append(paddleCancels, id)
			return &appdto.PaddleSubscription{ID: id, Status: "canceled"}, nil
		},
	}

	if err := s.CancelAllSubscriptionsImmediately(context.Background(), userID); err != nil {
		t.Fatalf("cancel all failed: %v", err)
	}
	if len(paddleCancels) != 1 || paddleCancels[0] != "sub_active" {
		t.Errorf("paddle cancels = %v, want exactly [sub_active] (trial is local, canceled is skipped)", paddleCancels)
	}
	if len(clamps) != 2 || clamps[0] != "sub_active" || clamps[1] != trialID {
		t.Errorf("canceled writes = %v, want [sub_active %s]", clamps, trialID)
	}
	if recomputes != 2 {
		t.Errorf("recomputes = %d, want 2 (one per canceled doc)", recomputes)
	}
}

// The first error aborts the loop: the local trial's authoritative write
// fails, so the following Paddle sub must not be touched (the mockPaddle
// panics on any call). The admin retries the whole cascade.
func TestCancelAllSubscriptionsImmediately_AbortsOnFirstError(t *testing.T) {
	userID := primitive.NewObjectID()
	trialID := models.LocalTrialSubscriptionID(userID, models.AppIDIndexly)

	m := &mockStore{
		findSubsByUser: func(ctx context.Context, uid primitive.ObjectID) ([]models.Subscription, error) {
			return []models.Subscription{
				{UserID: uid, PaddleSubscriptionID: trialID, Status: models.SubStatusTrialing, Source: models.SubSourceTrial},
				{UserID: uid, PaddleSubscriptionID: "sub_active", Status: models.SubStatusActive},
			}, nil
		},
		setCanceled: func(ctx context.Context, id string, at time.Time) error {
			return errors.New("mongo down")
		},
	}
	s := newTestService(t, m)
	s.paddle = &mockPaddle{} // any Paddle call panics = second sub was touched

	if err := s.CancelAllSubscriptionsImmediately(context.Background(), userID); err == nil {
		t.Fatal("expected error from failed trial cancel, got nil")
	}
}

// A subscription event for a tombstoned (admin-hard-deleted) doc must be
// note-acked without ever reaching the upsert — otherwise the late webhook
// would resurrect the deleted doc.
func TestWebhookSubscriptionEvent_TombstonedIsSkipped(t *testing.T) {
	var note string
	m := &mockStore{
		isSubTombstoned: func(ctx context.Context, id string) (bool, error) {
			return id == "sub_deleted", nil
		},
		markProcessed: func(ctx context.Context, eventID, n string) error {
			note = n
			return nil
		},
		// applySubscription/findCustomer/recompute unset: any call panics.
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCanceled, map[string]any{"id": "sub_deleted"})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("tombstoned event must ack (nil), got: %v", err)
	}
	if note == "" {
		t.Error("expected a note-ack explaining the tombstone skip, got empty note")
	}
}

// A transient tombstone-check failure must fail the event (no processed_at)
// so Paddle redelivers — never fall through to the upsert.
func TestWebhookSubscriptionEvent_TombstoneCheckErrorFails(t *testing.T) {
	failed := false
	m := &mockStore{
		isSubTombstoned: func(ctx context.Context, id string) (bool, error) {
			return false, errors.New("mongo down")
		},
		markFailed: func(ctx context.Context, eventID, errMsg string) error {
			failed = true
			return nil
		},
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCanceled, map[string]any{"id": "sub_x"})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("expected error so Paddle redelivers, got nil")
	}
	if !failed {
		t.Error("expected MarkEventFailed to record the transient error")
	}
}
