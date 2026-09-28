package service

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// CancelSubscriptionImmediately terminates the subscription at Paddle right now
// instead of at period end, and clamps access to the same moment. Called when the
// account itself disappears (Clerk user.deleted): the user can no longer
// authenticate, so there is no paid-through window worth honouring — and leaving
// the subscription live would keep billing a deleted account.
//
// Idempotent by design, because the Clerk webhook retries on failure: no
// subscription, an already-canceled one, or a Paddle-side cancel that already
// happened all return nil.
func (s *service) CancelSubscriptionImmediately(ctx context.Context, userID primitive.ObjectID) error {
	found, sub, err := s.store.FindLatestSubscriptionByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if !found || sub.Status == models.SubStatusCanceled {
		return nil
	}

	return s.cancelOneImmediately(ctx, userID, sub)
}

// CancelAllSubscriptionsImmediately is the admin-deletion variant: every
// non-canceled subscription doc the user holds (any app, paid or trial) is
// canceled now. The first error aborts — already-canceled docs are skipped on
// the retry, so the loop converges.
func (s *service) CancelAllSubscriptionsImmediately(ctx context.Context, userID primitive.ObjectID) error {
	subs, err := s.store.FindSubscriptionsByUserID(ctx, userID)
	if err != nil {
		return err
	}
	for i := range subs {
		sub := &subs[i]
		if sub.Status == models.SubStatusCanceled {
			continue
		}
		if err := s.cancelOneImmediately(ctx, userID, sub); err != nil {
			return err
		}
	}
	return nil
}

// cancelOneImmediately terminates one known-live subscription right now: local
// trials are ended with an authoritative Mongo write, Paddle subscriptions via
// the cancel-now API with the already-canceled-at-Paddle recovery check.
func (s *service) cancelOneImmediately(ctx context.Context, userID primitive.ObjectID, sub *models.Subscription) error {
	if sub.IsLocalTrial() {
		// No Paddle object behind a card-less trial. The local write is
		// authoritative (no webhook heals it) — return errors so the Clerk
		// webhook retries.
		if err := s.store.SetSubscriptionCanceled(ctx, sub.PaddleSubscriptionID, time.Now().UTC()); err != nil {
			return err
		}
		s.recomputeAfterLocalWrite(ctx, userID, sub)
		log.Info("local trial canceled immediately", "user_id", userID.Hex(), "subscription_id", sub.PaddleSubscriptionID)
		return nil
	}

	res, err := s.paddle.CancelImmediately(ctx, sub.PaddleSubscriptionID)
	if err != nil {
		// Local state may be stale (a dashboard cancel, or an earlier attempt of
		// this same webhook whose optimistic write failed). Only treat the error
		// as terminal if Paddle still shows the subscription as live.
		cur, gerr := s.paddle.GetSubscription(ctx, sub.PaddleSubscriptionID)
		if gerr != nil || cur.Status != paddleStatusCanceled {
			return fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		res = cur
	}

	canceledAt := time.Now().UTC()
	if res.CanceledAt != nil {
		canceledAt = res.CanceledAt.UTC()
	}

	// Optimistic write — does NOT advance last_event_at, so the confirming
	// subscription.canceled webhook stays authoritative.
	if err := s.store.SetSubscriptionCanceled(ctx, sub.PaddleSubscriptionID, canceledAt); err != nil {
		// Paddle accepted the cancel; the webhook will reconcile. Billing has
		// already stopped, so don't fail the caller over a local write.
		log.Error("optimistic canceled write failed; webhook will reconcile", "error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
	s.recomputeAfterLocalWrite(ctx, userID, sub)

	log.Info("subscription canceled immediately", "user_id", userID.Hex(), "paddle_subscription_id", sub.PaddleSubscriptionID)
	return nil
}

// CancelSubscription schedules cancellation at the end of the current billing
// period. Access continues until validTill (paid-through rule).
func (s *service) CancelSubscription(ctx context.Context, userID primitive.ObjectID) (*dto.CancelResponse, error) {
	found, sub, err := s.store.FindLatestSubscriptionByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found || !time.Now().UTC().Before(sub.ValidTill) {
		return nil, ErrNoActiveSubscription
	}

	// Already canceling — idempotent no-op rather than a Paddle error.
	if sub.Status == models.SubStatusCanceling {
		return &dto.CancelResponse{Status: sub.Status, ValidTill: sub.ValidTill, ScheduledCancelAt: sub.ScheduledCancelAt}, nil
	}

	if sub.IsLocalTrial() {
		// Nothing to stop billing on — mirror the period-end idiom so the
		// trial runs out its window. Authoritative local write: error out
		// rather than log (no webhook reconciles trials).
		scheduledAt := sub.ValidTill
		if err := s.store.SetSubscriptionCanceling(ctx, sub.PaddleSubscriptionID, &scheduledAt); err != nil {
			return nil, err
		}
		s.recomputeAfterLocalWrite(ctx, userID, sub)
		return &dto.CancelResponse{
			Status:            models.SubStatusCanceling,
			ValidTill:         sub.ValidTill,
			ScheduledCancelAt: &scheduledAt,
		}, nil
	}

	res, err := s.paddle.CancelAtPeriodEnd(ctx, sub.PaddleSubscriptionID)
	if err != nil {
		// Local state may be stale (e.g. dashboard-initiated cancel whose
		// webhook hasn't landed). If Paddle already has the scheduled
		// cancellation, treat as success.
		cur, gerr := s.paddle.GetSubscription(ctx, sub.PaddleSubscriptionID)
		if gerr != nil || cur.ScheduledCancelAt == nil {
			return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		res = cur
	}

	scheduledAt := res.ScheduledCancelAt
	if scheduledAt == nil {
		// Paddle schedules the cancel for period end; fall back to validTill
		// if the response omitted it.
		scheduledAt = &sub.ValidTill
	}

	// Optimistic write — does NOT advance last_event_at, so the confirming
	// subscription.updated webhook (or a delayed earlier one) remains
	// authoritative. A transient status flap until it lands is expected.
	if err := s.store.SetSubscriptionCanceling(ctx, sub.PaddleSubscriptionID, scheduledAt); err != nil {
		// Paddle accepted the cancel; webhook will reconcile. Don't fail the request.
		log.Error("optimistic canceling write failed; webhook will reconcile", "error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
	s.recomputeAfterLocalWrite(ctx, userID, sub)

	return &dto.CancelResponse{
		Status:            models.SubStatusCanceling,
		ValidTill:         sub.ValidTill,
		ScheduledCancelAt: scheduledAt,
	}, nil
}
