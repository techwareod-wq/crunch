package service

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/paymentService"
	"github.com/atharva-ng/crunch/internal/services/paymentService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Sentinel errors mapped to HTTP codes by the payments controller.
var (
	ErrNoActiveSubscription      = errors.New("no active subscription")
	ErrSubscriptionAlreadyActive = errors.New("subscription already active")
	ErrEmailRequired             = errors.New("user email is required for checkout")
	ErrPaymentProvider           = errors.New("payment provider request failed")
	// ErrTrialNotAvailable: start-trial against an unknown price or a plan
	// with no card-less trial (trial_days unset).
	ErrTrialNotAvailable = errors.New("plan does not offer a card-less trial")
	// ErrTrialAlreadyUsed: the identity has prior subscription history (any
	// source, any state) — the one-trial-ever rule.
	ErrTrialAlreadyUsed = errors.New("free trial already used")
	// ErrTrialNotConvertible: a card-less trial has no payment method, so
	// "end trial → convert" cannot bill it.
	ErrTrialNotConvertible = errors.New("card-less trial cannot be converted; checkout required")
	// ErrOnboardingIncomplete: the user has no finalised web entity, so the
	// card-less trial precondition (site set up) is unmet.
	ErrOnboardingIncomplete = errors.New("onboarding incomplete; finalise the web entity before starting a trial")
	// ErrNoTrialingSubscription: the target has no subscription in trialing
	// status for the app — trial admin ops apply to live trials only.
	ErrNoTrialingSubscription = errors.New("no trialing subscription")

	// Company (team seat) billing sentinels.
	// ErrTeamPlanNotAvailable: PADDLE_TEAM_PRODUCT_ID is unset — the feature
	// ships dark; the checkout guard is decision 7's single enforcement point.
	ErrTeamPlanNotAvailable = errors.New("team plan not available")
	ErrCompanyNotFound      = errors.New("company not found")
	ErrNotCompanyOwner      = errors.New("caller does not own the company")
	// ErrCompanyNotTeam: personal companies never buy seats.
	ErrCompanyNotTeam = errors.New("company is not a team company")
	// ErrCompanyWebsiteRequired: D14 — the website URL is the company's learn
	// source and must be set before purchase.
	ErrCompanyWebsiteRequired = errors.New("company website URL required before purchase")
)

type service struct {
	store  store.Store
	paddle interfaces.PaddleClient
	// plans resolves webhook prices to apps (fail-closed app_id stamping).
	plans *entitlements.PlansCache
	// dispatcher carries trial lifecycle side-effects (converted/expired)
	// onto the async queue; delivery is at-least-once (two-marker pattern).
	dispatcher interfaces.Dispatcher
	// productID is passed as a plain value (not a config struct) so this
	// package never imports internal/config — config imports the service
	// root for the interface, and a config import here would cycle.
	productID string
	// teamProductID is the per-seat "Indexly Team" product. EMPTY IS A
	// SUPPORTED STEADY STATE (ships dark pre-Paddle, decision 7): the company
	// checkout guard 400s on it and nothing else ever consults it except the
	// individual checkout's lag-guard product filter, which no-ops while empty.
	teamProductID string
	// trialMaxArticles is values.yaml siteIntelligence.trial.maxArticles,
	// passed as a plain value for the same import-cycle reason as productID.
	// Surfaced on /payments/status as the free-plan article meter cap.
	trialMaxArticles int
}

func NewService(st store.Store, paddle interfaces.PaddleClient, plans *entitlements.PlansCache, dispatcher interfaces.Dispatcher, productID, teamProductID string, trialMaxArticles int) paymentService.PaymentService {
	return &service{store: st, paddle: paddle, plans: plans, dispatcher: dispatcher, productID: productID, teamProductID: teamProductID, trialMaxArticles: trialMaxArticles}
}

// recomputeAfterLocalWrite follows every optimistic subscriptions write
// (cancel/un-cancel paths). Without it the write would be invisible to the
// projection-backed gate until the confirming webhook lands — hours-to-days in
// a webhook outage — losing today's immediate-revocation property. Failure is
// logged, not returned: like the optimistic write itself, the webhook (and
// the admin recompute) reconcile.
func (s *service) recomputeAfterLocalWrite(ctx context.Context, userID primitive.ObjectID, sub *models.Subscription) {
	appID := sub.AppID
	if appID == "" {
		appID = models.AppIDIndexly // docs from before the backfill stamped app_id
	}
	if err := s.store.RecomputeUserEntitlement(ctx, userID, appID); err != nil {
		log.Error("entitlement recompute after local write failed; webhook will reconcile",
			"error", err, "user_id", userID.Hex(), "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
}
