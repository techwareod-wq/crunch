package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
)

func trialingSub(userID primitive.ObjectID) *models.Subscription {
	return &models.Subscription{
		UserID:               userID,
		AppID:                models.AppIDIndexly,
		PaddleSubscriptionID: "sub_trial",
		Status:               models.SubStatusTrialing,
		ValidTill:            time.Now().UTC().Add(72 * time.Hour),
		WasTrialing:          true,
	}
}

func TestExtendTrial_MovesNextBilledAtFromTrialEnd(t *testing.T) {
	userID := primitive.NewObjectID()
	sub := trialingSub(userID)
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			if appID != models.AppIDIndexly {
				t.Errorf("appID = %q, want indexly (empty appId defaults)", appID)
			}
			return true, sub, nil
		},
	}
	s := newTestService(t, m)
	var requested time.Time
	s.paddle = &mockPaddle{setNextBilledAt: func(ctx context.Context, id string, at time.Time) (*appdto.PaddleSubscription, error) {
		requested = at
		return &appdto.PaddleSubscription{ID: id, Status: "trialing", CurrentPeriodEndsAt: &at}, nil
	}}

	got, err := s.ExtendTrial(context.Background(), userID, "", 7)
	if err != nil {
		t.Fatalf("extend failed: %v", err)
	}
	want := sub.ValidTill.AddDate(0, 0, 7)
	if !requested.Equal(want) {
		t.Errorf("paddle next_billed_at = %v, want trial end + 7d = %v", requested, want)
	}
	if got == nil || !got.Equal(want) {
		t.Errorf("returned trial end = %v, want %v", got, want)
	}
}

func TestExtendTrial_NoTrialingSubscription(t *testing.T) {
	userID := primitive.NewObjectID()
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, &models.Subscription{Status: models.SubStatusActive, PaddleSubscriptionID: "sub_1"}, nil
		},
	}
	s := newTestService(t, m)

	if _, err := s.ExtendTrial(context.Background(), userID, "", 7); !errors.Is(err, ErrNoTrialingSubscription) {
		t.Fatalf("active sub must not be extendable as a trial, got: %v", err)
	}
}

func TestEndTrial_ConvertActivatesAndRecomputes(t *testing.T) {
	userID := primitive.NewObjectID()
	var calls []string
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, trialingSub(userID), nil
		},
		setActive: func(ctx context.Context, id string) error {
			calls = append(calls, "activate-local")
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			calls = append(calls, "recompute")
			return nil
		},
	}
	s := newTestService(t, m)
	s.paddle = &mockPaddle{activateTrialing: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
		calls = append(calls, "paddle-activate")
		return &appdto.PaddleSubscription{ID: id, Status: "active"}, nil
	}}

	if err := s.EndTrial(context.Background(), userID, "", TrialEndModeConvert); err != nil {
		t.Fatalf("end trial convert failed: %v", err)
	}
	if got := fmt.Sprintf("%v", calls); got != "[paddle-activate activate-local recompute]" {
		t.Errorf("call order = %v, want paddle → local write → recompute", calls)
	}
}

func TestEndTrial_CancelClampsAndRecomputes(t *testing.T) {
	userID := primitive.NewObjectID()
	var calls []string
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, trialingSub(userID), nil
		},
		setCanceled: func(ctx context.Context, id string, at time.Time) error {
			calls = append(calls, "clamp")
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			calls = append(calls, "recompute")
			return nil
		},
	}
	s := newTestService(t, m)
	s.paddle = &mockPaddle{cancelImmediately: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
		calls = append(calls, "paddle-cancel")
		return &appdto.PaddleSubscription{ID: id, Status: "canceled"}, nil
	}}

	if err := s.EndTrial(context.Background(), userID, "", TrialEndModeCancel); err != nil {
		t.Fatalf("end trial cancel failed: %v", err)
	}
	if got := fmt.Sprintf("%v", calls); got != "[paddle-cancel clamp recompute]" {
		t.Errorf("call order = %v, want paddle → clamp → recompute", calls)
	}
}

func TestEndTrial_InvalidMode(t *testing.T) {
	userID := primitive.NewObjectID()
	m := &mockStore{
		findLatestForApp: func(ctx context.Context, uid primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
			return true, trialingSub(userID), nil
		},
	}
	s := newTestService(t, m)

	if err := s.EndTrial(context.Background(), userID, "", "pause"); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}
