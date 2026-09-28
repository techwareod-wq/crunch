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

// Resubscribe un-cancels a still-valid canceling subscription (same Paddle
// subscription, next bill at current period end — no double billing). When
// the old subscription has fully lapsed, it directs the frontend through a
// fresh checkout, which produces a new subscription document.
func (s *service) Resubscribe(ctx context.Context, userID primitive.ObjectID) (*dto.ResubscribeResponse, error) {
	found, sub, err := s.store.FindLatestSubscriptionByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found || !time.Now().UTC().Before(sub.ValidTill) {
		return &dto.ResubscribeResponse{Mode: dto.ResubscribeModeCheckoutRequired}, nil
	}

	if sub.Status != models.SubStatusCanceling {
		// active / past_due within the paid period: nothing to resubscribe to.
		return nil, ErrSubscriptionAlreadyActive
	}

	if sub.IsLocalTrial() {
		// Un-cancel restores trialing (not active — SetSubscriptionActive
		// would over-grant paid features with no webhook to correct it).
		// Authoritative local write: return errors, don't log them.
		if err := s.store.SetSubscriptionTrialing(ctx, sub.PaddleSubscriptionID); err != nil {
			return nil, err
		}
		s.recomputeAfterLocalWrite(ctx, userID, sub)
		return &dto.ResubscribeResponse{Mode: dto.ResubscribeModeUncanceled, ValidTill: &sub.ValidTill}, nil
	}

	if _, err := s.paddle.RemoveScheduledCancellation(ctx, sub.PaddleSubscriptionID); err != nil {
		// Disambiguate: the scheduled change may have already executed
		// (period ended seconds ago) — that's checkout territory, not a 502.
		cur, gerr := s.paddle.GetSubscription(ctx, sub.PaddleSubscriptionID)
		switch {
		case gerr == nil && cur.Status == paddleStatusCanceled:
			return &dto.ResubscribeResponse{Mode: dto.ResubscribeModeCheckoutRequired}, nil
		case gerr == nil && cur.Status == paddleStatusActive && cur.ScheduledCancelAt == nil:
			// already un-canceled (idempotent retry) — proceed
		default:
			return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
	}

	// Optimistic write — last_event_at untouched; subscription.updated confirms.
	if err := s.store.SetSubscriptionActive(ctx, sub.PaddleSubscriptionID); err != nil {
		log.Error("optimistic un-cancel write failed; webhook will reconcile", "error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
	s.recomputeAfterLocalWrite(ctx, userID, sub)

	return &dto.ResubscribeResponse{
		Mode:      dto.ResubscribeModeUncanceled,
		ValidTill: &sub.ValidTill,
	}, nil
}
