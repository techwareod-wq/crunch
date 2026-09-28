package service

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func (s *service) GetPlans(ctx context.Context, userID primitive.ObjectID) ([]dto.PlanDTO, error) {
	prices, err := s.paddle.ListActivePrices(ctx, s.productID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
	}

	eligible, err := s.trialEligible(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Two gates compose here: offeredPrices keeps only prices mapped in the
	// system catalog (a Paddle-live but unmapped trial price is never sold),
	// then selectPricesForUser picks trial vs no-trial by eligibility. So the
	// Paddle trial is shown only when BOTH the user is eligible AND the catalog
	// carries a trial variant — retire the trial variant from plans.json and the
	// trial disappears regardless of what Paddle still lists.
	selected := s.selectPricesForUser(s.offeredPrices(prices), eligible, userID)

	plans := make([]dto.PlanDTO, 0, len(selected))
	for _, p := range selected {
		plan := dto.PlanDTO{
			PriceID:          p.ID,
			ProductID:        p.ProductID,
			Name:             p.Name,
			Description:      p.Description,
			UnitAmount:       p.UnitAmount,
			CurrencyCode:     p.CurrencyCode,
			BillingInterval:  p.BillingInterval,
			BillingFrequency: p.BillingFrequency,
			TrialInterval:    p.TrialInterval,
			TrialFrequency:   p.TrialFrequency,
		}
		// Offer the card-less trial only to eligible users (same gate as the
		// Paddle trial price) and only for plans configured with trial_days.
		// The free plan never lapses by date, so the article cap rides along
		// for the "N free articles" copy — trial_days remains the on/off flag.
		if eligible {
			if catalog, ok := s.plans.ByPriceID(p.ID); ok && catalog.TrialDays > 0 {
				plan.CardlessTrialDays = catalog.TrialDays
				plan.CardlessTrialArticles = s.trialMaxArticles
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// trialEligible reports whether the user's whole identity has never had a
// subscription. The free trial is consumed on the first subscription, so any
// prior record — active, canceled, or expired — means the trial is spent.
//
// The check is identity-wide (same as the webhook's re-trial guard), not just
// the live user's own subscriptions: a user who consumed the trial under an
// account that was later deactivated re-onboards with a fresh user _id but the
// same email, so a per-user-id lookup would find nothing and wrongly re-offer
// the trial price. TrialEligibleIdentity matches same-email tombstones (and the
// Paddle customer), so a tombstoned trial-consumer is correctly shown only the
// no-trial price during onboarding.
func (s *service) trialEligible(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	return s.store.TrialEligibleIdentity(ctx, userID, "", "")
}

// offeredPrices narrows the product's live Paddle prices to those mapped in
// the plan catalog — the system's source of truth for what may be sold.
//
// The catalog is the gate, not Paddle's active list, because the webhook fails
// closed on unmapped prices: a price that's live in Paddle but absent from the
// catalog (e.g. a retired trial price) would, if offered, take a payment we
// can't turn into access. Filtering here means retiring a variant from
// plans.json is sufficient to stop offering it, regardless of Paddle state.
// This is also what makes the trial's visibility catalog-driven: drop the
// trial variant and no trial price survives for selectPricesForUser to pick.
func (s *service) offeredPrices(prices []appdto.PaddlePrice) []appdto.PaddlePrice {
	offered := make([]appdto.PaddlePrice, 0, len(prices))
	for _, p := range prices {
		if _, ok := s.plans.ByPriceID(p.ID); ok {
			offered = append(offered, p)
		}
	}
	return offered
}

// selectPricesForUser picks, from the catalog-offered prices, the ones the user
// should be shown. Paddle carries a trial price and a no-trial price for the
// same amount (distinguished by TrialInterval); trial-eligible users are
// offered the trial price, everyone else the no-trial one — that's the price
// the overlay opens against, so Paddle never re-grants the trial.
//
// Because it runs on the catalog-filtered set, "is a trial price present?" is
// already a system-catalog decision: if plans.json has no trial variant, the
// withTrial bucket is empty and even an eligible user gets the no-trial price.
//
// If the preferred bucket is unconfigured we fall back to the other so checkout
// never breaks. The one unsafe case — an ineligible user with no no-trial price
// — would re-grant the trial, so it's logged loudly as a misconfiguration.
func (s *service) selectPricesForUser(prices []appdto.PaddlePrice, eligible bool, userID primitive.ObjectID) []appdto.PaddlePrice {
	var withTrial, withoutTrial []appdto.PaddlePrice
	for _, p := range prices {
		if p.TrialInterval != "" {
			withTrial = append(withTrial, p)
		} else {
			withoutTrial = append(withoutTrial, p)
		}
	}

	if eligible {
		if len(withTrial) > 0 {
			return withTrial
		}
		return withoutTrial
	}

	if len(withoutTrial) > 0 {
		return withoutTrial
	}
	if len(withTrial) > 0 {
		log.Warn("no no-trial price configured; returning user would be re-granted the trial",
			"product_id", s.productID, "user_id", userID.Hex())
	}
	return withTrial
}
