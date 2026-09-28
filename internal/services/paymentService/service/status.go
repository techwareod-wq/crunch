package service

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

// GetStatus reports billing state. The legacy Subscription field keeps its
// store-read Indexly shape; the per-app Apps map is built purely from the
// caller-supplied user doc's entitlement projection (no extra store read).
func (s *service) GetStatus(ctx context.Context, user *models.User) (*dto.PaymentStatusResponse, error) {
	now := time.Now().UTC()
	resp := &dto.PaymentStatusResponse{
		Apps: buildAppsAccess(user, s.plans, now),
	}

	found, sub, err := s.store.FindLatestSubscriptionByUserID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return resp, nil
	}

	isValid := now.Before(sub.ValidTill)

	// Derive "expired" purely from validTill so the UI agrees with the
	// middleware even when a renewal webhook was missed and the stored
	// status still says active.
	status := sub.Status
	if !isValid {
		status = "expired"
	}

	resp.HasSubscription = true
	resp.Subscription = &dto.SubscriptionDTO{
		Status:            status,
		PriceID:           sub.PriceID,
		ValidTill:         sub.ValidTill,
		ScheduledCancelAt: sub.ScheduledCancelAt,
		IsValid:           isValid,
		IsLocalTrial:      sub.IsLocalTrial(),
	}

	// Free-plan article meter: the trial never lapses by date, so the banner
	// shows articles left, not days left. Only attached while the card-less
	// trial is live — paid subs are uncapped.
	if sub.IsLocalTrial() && status == models.SubStatusTrialing {
		used, err := s.store.GetLifetimeArticlesGenerated(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		resp.TrialUsage = &dto.TrialUsageDTO{ArticlesUsed: used, MaxArticles: s.trialMaxArticles}
	}
	return resp, nil
}

// buildAppsAccess derives one AppAccessDTO per app in the union of the plan
// catalog's app IDs and the user's entitlement keys. Pure over its inputs
// (unit-test target).
func buildAppsAccess(user *models.User, plans *entitlements.PlansCache, now time.Time) map[string]dto.AppAccessDTO {
	appIDs := map[string]bool{}
	for _, id := range plans.AppIDs() {
		appIDs[id] = true
	}
	if user != nil {
		for id := range user.Entitlements {
			appIDs[id] = true
		}
	}

	apps := make(map[string]dto.AppAccessDTO, len(appIDs))
	for appID := range appIDs {
		apps[appID] = appAccess(user, appID, plans, now)
	}
	return apps
}

func appAccess(user *models.User, appID string, plans *entitlements.PlansCache, now time.Time) dto.AppAccessDTO {
	var ent models.AppEntitlement
	if user != nil {
		ent = user.Entitlements[appID]
	}
	subValid := now.Before(ent.ValidTill)

	features := entitlements.EffectiveFeatures(user, appID, plans, now)
	if features == nil {
		features = []string{}
	}

	switch {
	case subValid:
		out := dto.AppAccessDTO{Status: ent.Status, Features: features}
		v := ent.ValidTill
		out.ValidTill = &v
		if plan, ok := plans.ByPriceID(ent.PriceID); ok {
			out.Tier = plan.Tier
		}
		return out
	case !ent.ValidTill.IsZero():
		// Had a subscription once; the paid-through boundary has passed.
		// Same derivation rule as the legacy DTO.
		v := ent.ValidTill
		return dto.AppAccessDTO{Status: "expired", ValidTill: &v, Features: features}
	default:
		return dto.AppAccessDTO{Status: "none", Features: features}
	}
}

func (s *service) HasValidSubscription(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	return s.store.HasValidSubscription(ctx, userID)
}
