package entitlements

import (
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

// Decision is the resolver's verdict; the middlewares map it onto the wire
// (402 subscription_required / 402 feature_not_included).
type Decision int

const (
	Allow              Decision = iota
	NoSubscription              // → ErrSubscriptionRequired (402, code subscription_required)
	FeatureNotIncluded          // → apperrors.FeatureNotIncluded (402, code feature_not_included)
)

// String keeps a logged verdict readable — a bare iota int in a log line tells
// whoever is debugging a denied request nothing about which denial it was.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case NoSubscription:
		return "no_subscription"
	case FeatureNotIncluded:
		return "feature_not_included"
	default:
		return "unknown"
	}
}

// Resolve implements the request-time authorization algorithm over the
// JWT-loaded user's entitlement projection — pure in-memory, no DB. now is
// injected for tests.
//
// Access validity ignores Status (parity with the old count-query gate, which
// only checked valid_till — canceling/past_due users keep access through the
// period they paid for); Status selects the trial vs paid feature list.
// feature == "" is the app-level gate and never touches the plans cache —
// parity does not depend on plan data.
func Resolve(u *models.User, app string, feature Feature, plans *PlansCache, now time.Time) Decision {
	if u == nil {
		return NoSubscription
	}
	ent, ok := u.Entitlements[app]
	if !ok {
		return NoSubscription
	}
	if !now.Before(ent.ValidTill) {
		// No valid subscription backs the entitlement (absent projection or a
		// lapsed one). An active admin comp grants access as-if-subscribed to
		// the app's active paid tier — the cutover escape hatch for hand-picked
		// users (§3.7 of the LinkedIn billing plan).
		if ent.Comp.Active(now) {
			if feature == "" {
				return Allow
			}
			if compSet(ent, app, plans, now)[string(feature)] {
				return Allow
			}
			return FeatureNotIncluded
		}
		// Expired entitlement — either a timed-out local trial (status stays
		// trialing, no webhook ever moves it) or a lapsed paid sub
		// (canceled/past_due/paused/etc. past its paid-through date). Both fall
		// back to the view-only feature set: the user keeps READ access to what
		// they produced, but every write 402s. Purely a resolver rule — no
		// writes, no sweeper. The bare app gate (feature == "") still denies, so
		// routes gated only on subscription validity stay closed. A later valid
		// sub naturally wins by passing the validity check above.
		if feature != "" {
			if expiredViewSet(ent, app, plans, now)[string(feature)] {
				return Allow
			}
			return FeatureNotIncluded
		}
		return NoSubscription
	}
	if feature == "" {
		return Allow // app-level gate only
	}

	if effectiveSet(ent, plans, now)[string(feature)] {
		return Allow
	}
	return FeatureNotIncluded
}

// EffectiveFeatures returns the caller's effective feature set for the app —
// plan list (trial list while trialing) ± live overrides — or nil when they
// have no access at all. An expired entitlement (timed-out trial OR lapsed paid
// sub) reports the view-only set, mirroring Resolve, so the frontend can detect
// the read-only state from the features array. Sorted for stable output (admin
// + status DTOs).
func EffectiveFeatures(u *models.User, app string, plans *PlansCache, now time.Time) []string {
	if u == nil {
		return nil
	}
	ent, ok := u.Entitlements[app]
	if !ok {
		return nil
	}
	var set map[string]bool
	switch {
	case now.Before(ent.ValidTill):
		set = effectiveSet(ent, plans, now)
	case ent.Comp.Active(now):
		set = compSet(ent, app, plans, now)
	default:
		set = expiredViewSet(ent, app, plans, now)
	}
	features := make([]string, 0, len(set))
	for f := range set {
		features = append(features, f)
	}
	sort.Strings(features)
	return features
}

// expiredViewSet computes the feature set for an EXPIRED entitlement (a
// timed-out trial or a lapsed paid sub): the trial_expired plan's view-only
// list (resolved by (app, tier) — the virtual plan has no price), still ± live
// grants/revokes so an admin grant keeps working past expiry. Missing plan doc
// fails closed to grants-only, same as an unknown price in effectiveSet.
func expiredViewSet(ent models.AppEntitlement, app string, plans *PlansCache, now time.Time) map[string]bool {
	set := map[string]bool{}
	if plan, ok := plans.ByAppTier(app, TierTrialExpired); ok {
		for _, f := range plan.Features {
			set[f] = true
		}
	}
	for _, g := range ent.Grants {
		if g.ExpiresAt == nil || now.Before(*g.ExpiresAt) {
			set[g.Key] = true
		}
	}
	for _, r := range ent.Revokes {
		if r.ExpiresAt == nil || now.Before(*r.ExpiresAt) {
			delete(set, r.Key)
		}
	}
	return set
}

// compSet computes the feature set for an admin-comped entitlement with no
// valid subscription behind it: the app's active paid tier's full feature
// list, still ± live grants/revokes. Missing active plan fails closed to
// grants-only (the app-level Allow already fired — comp holders keep access
// even before the catalog is seeded, they just resolve no features).
func compSet(ent models.AppEntitlement, app string, plans *PlansCache, now time.Time) map[string]bool {
	set := map[string]bool{}
	if plan, ok := plans.ActivePaidPlanForApp(app); ok {
		for _, f := range plan.Features {
			set[f] = true
		}
	}
	for _, g := range ent.Grants {
		if g.ExpiresAt == nil || now.Before(*g.ExpiresAt) {
			set[g.Key] = true
		}
	}
	for _, r := range ent.Revokes {
		if r.ExpiresAt == nil || now.Before(*r.ExpiresAt) {
			delete(set, r.Key)
		}
	}
	return set
}

// effectiveSet computes the feature set for an entitlement whose access is
// already established (its subscription is valid).
func effectiveSet(ent models.AppEntitlement, plans *PlansCache, now time.Time) map[string]bool {
	set := map[string]bool{}
	if plan, ok := plans.ByPriceID(ent.PriceID); ok {
		list := plan.Features
		if ent.Status == models.SubStatusTrialing {
			list = plan.TrialFeatures
		}
		for _, f := range list {
			set[f] = true
		}
	}
	// Unknown price (plan deleted / not yet configured): base set stays
	// empty — fail closed on features, but grants still apply below.
	for _, g := range ent.Grants {
		if g.ExpiresAt == nil || now.Before(*g.ExpiresAt) {
			set[g.Key] = true
		}
	}
	for _, r := range ent.Revokes {
		if r.ExpiresAt == nil || now.Before(*r.ExpiresAt) {
			delete(set, r.Key)
		}
	}
	return set
}
