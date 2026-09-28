package service

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Trial end modes (admin "end trial now").
const (
	TrialEndModeConvert = "convert" // bill immediately, trial → paid
	TrialEndModeCancel  = "cancel"  // terminate now, no charge
)

// StartTrial provisions a card-less local trial for the plan owning priceID:
// a source:"trial" subscription doc with a synthetic paddle_subscription_id
// and a far-future valid_till — the free plan never lapses by date; only the
// lifetime article cap (values.yaml siteIntelligence.trial.maxArticles) gates
// generation. plan.trial_days > 0 remains the "trial offered at all"
// eligibility flag. No Paddle objects and no payment method exist; upgrading
// goes through normal checkout (trialEligible is false after the first trial,
// so only the no-trial price is offered — the trial is never re-granted).
func (s *service) StartTrial(ctx context.Context, userID primitive.ObjectID, priceID string) (*dto.StartTrialResponse, error) {
	plan, ok := s.plans.ByPriceID(priceID)
	if !ok || plan.TrialDays <= 0 {
		return nil, ErrTrialNotAvailable
	}

	// Onboarding gate: the trial lands the user on the dashboard, which is only
	// meaningful once the site is set up. No finalised web entity → no trial.
	finalised, err := s.store.IsWebEntityFinalised(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !finalised {
		return nil, ErrOnboardingIncomplete
	}

	// One trial ever, per identity (live user + same-email tombstones + the
	// Paddle customer, if one exists yet): any prior subscription of any
	// source or state — including an expired local trial — consumes it.
	paddleCustomerID := ""
	if found, customer, err := s.store.FindCustomerByUserID(ctx, userID); err != nil {
		return nil, err
	} else if found {
		paddleCustomerID = customer.PaddleCustomerID
	}
	eligible, err := s.store.TrialEligibleIdentity(ctx, userID, paddleCustomerID, "")
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, ErrTrialAlreadyUsed
	}

	now := time.Now().UTC()
	sub := &models.Subscription{
		UserID:               userID,
		AppID:                plan.AppID,
		PaddleSubscriptionID: models.LocalTrialSubscriptionID(userID, plan.AppID),
		Source:               models.SubSourceTrial,
		PriceID:              priceID,
		ProductID:            s.productID,
		Status:               models.SubStatusTrialing,
		WasTrialing:          true,
		ValidTill:            models.LocalTrialValidTill(now),
		StartedAt:            now,
		LastEventAt:          now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.store.InsertTrialSubscription(ctx, sub); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return nil, err
		}
		// The deterministic synthetic ID already exists — a retry or a
		// concurrent double-submit. A still-live trial makes the call
		// idempotent (and re-running the recompute below heals a projection
		// lost between insert and recompute); a lapsed one is spent.
		found, existing, ferr := s.store.FindSubscriptionByPaddleID(ctx, sub.PaddleSubscriptionID)
		if ferr != nil {
			return nil, ferr
		}
		if !found || existing.Status != models.SubStatusTrialing || !time.Now().UTC().Before(existing.ValidTill) {
			return nil, ErrTrialAlreadyUsed
		}
		sub = existing
	}
	s.recomputeAfterLocalWrite(ctx, userID, sub)

	log.Info("card-less trial started", "user_id", userID.Hex(), "app_id", plan.AppID,
		"price_id", priceID, "trial_days", plan.TrialDays, "valid_till", sub.ValidTill)
	return &dto.StartTrialResponse{AppID: plan.AppID, Status: models.SubStatusTrialing, ValidTill: sub.ValidTill}, nil
}

// findTrialingSubscription resolves the target's live trialing sub for the
// app (legacy docs without app_id scope to Indexly via the store query).
func (s *service) findTrialingSubscription(ctx context.Context, userID primitive.ObjectID, appID string) (*models.Subscription, error) {
	if appID == "" {
		appID = models.AppIDIndexly
	}
	found, sub, err := s.store.FindLatestSubscriptionByUserIDForApp(ctx, userID, appID)
	if err != nil {
		return nil, err
	}
	if !found || sub.Status != models.SubStatusTrialing {
		return nil, ErrNoTrialingSubscription
	}
	return sub, nil
}

// ExtendTrial pushes the trial's next billing date out by extendDays (from
// the current trial end, or from now if the stored end already passed). The
// confirming subscription.updated webhook carries the authoritative new
// period — no optimistic local write is needed because access is uninterrupted
// either way. Returns the requested new trial end.
func (s *service) ExtendTrial(ctx context.Context, userID primitive.ObjectID, appID string, extendDays int) (*time.Time, error) {
	sub, err := s.findTrialingSubscription(ctx, userID, appID)
	if err != nil {
		return nil, err
	}

	base := sub.ValidTill
	if now := time.Now().UTC(); base.Before(now) {
		base = now
	}
	next := base.AddDate(0, 0, extendDays)

	if sub.IsLocalTrial() {
		// No Paddle object and no confirming webhook — this write IS the
		// authoritative state, so failure is returned, not logged.
		if err := s.store.SetTrialValidTill(ctx, sub.PaddleSubscriptionID, next); err != nil {
			return nil, err
		}
		s.recomputeAfterLocalWrite(ctx, userID, sub)
		log.Info("local trial extended", "user_id", userID.Hex(), "subscription_id", sub.PaddleSubscriptionID,
			"extend_days", extendDays, "new_trial_end", next)
		return &next, nil
	}

	res, err := s.paddle.SetNextBilledAt(ctx, sub.PaddleSubscriptionID, next)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
	}
	if res.CurrentPeriodEndsAt != nil {
		next = *res.CurrentPeriodEndsAt
	}
	log.Info("trial extended", "user_id", userID.Hex(), "paddle_subscription_id", sub.PaddleSubscriptionID,
		"extend_days", extendDays, "new_trial_end", next)
	return &next, nil
}

// EndTrial terminates a trial now: mode "convert" bills immediately (raw
// status flips to active → the webhook claims the conversion), mode "cancel"
// terminates with no charge. Both follow the cancel/resubscribe idiom —
// Paddle call + optimistic local write + recompute; the webhook is the
// authority.
func (s *service) EndTrial(ctx context.Context, userID primitive.ObjectID, appID, mode string) error {
	sub, err := s.findTrialingSubscription(ctx, userID, appID)
	if err != nil {
		return err
	}

	if sub.IsLocalTrial() {
		switch mode {
		case TrialEndModeConvert:
			// A card-less trial has no payment method — nothing to bill.
			return ErrTrialNotConvertible
		case TrialEndModeCancel:
			// Authoritative local write (no webhook heals trials) — return
			// the error rather than logging it.
			if err := s.store.SetSubscriptionCanceled(ctx, sub.PaddleSubscriptionID, time.Now().UTC()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid trial end mode %q", mode)
		}
		s.recomputeAfterLocalWrite(ctx, userID, sub)
		log.Info("local trial ended", "user_id", userID.Hex(), "subscription_id", sub.PaddleSubscriptionID, "mode", mode)
		return nil
	}

	switch mode {
	case TrialEndModeConvert:
		if _, err := s.paddle.ActivateTrialingSubscription(ctx, sub.PaddleSubscriptionID); err != nil {
			return fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		// Optimistic status flip; the webhook confirms and also claims the
		// trial conversion off the raw "active" status.
		if err := s.store.SetSubscriptionActive(ctx, sub.PaddleSubscriptionID); err != nil {
			log.Error("optimistic trial-convert write failed; webhook will reconcile",
				"error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
		}
	case TrialEndModeCancel:
		res, err := s.paddle.CancelImmediately(ctx, sub.PaddleSubscriptionID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		canceledAt := time.Now().UTC()
		if res.CanceledAt != nil {
			canceledAt = res.CanceledAt.UTC()
		}
		if err := s.store.SetSubscriptionCanceled(ctx, sub.PaddleSubscriptionID, canceledAt); err != nil {
			log.Error("optimistic trial-cancel write failed; webhook will reconcile",
				"error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
		}
	default:
		return fmt.Errorf("invalid trial end mode %q", mode)
	}

	s.recomputeAfterLocalWrite(ctx, userID, sub)
	log.Info("trial ended", "user_id", userID.Hex(), "paddle_subscription_id", sub.PaddleSubscriptionID, "mode", mode)
	return nil
}
