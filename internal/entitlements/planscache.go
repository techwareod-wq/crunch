package entitlements

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// PlansCache is the in-memory plan catalog: Country Catalog pattern EXTENDED
// with refresh (the catalog itself is load-once). Write-through Reload on
// admin plan writes PLUS a background ticker — the ticker bounds staleness for
// any process that didn't serve the admin write (rolling deploys briefly run
// two processes).
//
// It holds ALL plans including inactive ones: Active gates purchasability,
// never resolution.
type PlansCache struct {
	mu      sync.RWMutex
	byPrice map[string]*models.Plan // price_id → plan
	byApp   map[string][]*models.Plan
}

// NewPlansCache boot-loads the catalog. A DB error hard-fails startup (a
// service that can't resolve plans would quarantine every webhook), but zero
// docs is fine — the binary deploys before the seed run.
func NewPlansCache(ctx context.Context) (*PlansCache, error) {
	c := &PlansCache{byPrice: map[string]*models.Plan{}, byApp: map[string][]*models.Plan{}}
	if err := c.Reload(ctx); err != nil {
		return nil, fmt.Errorf("plans cache boot load: %w", err)
	}
	return c, nil
}

// NewPlansCacheFromPlans builds a cache from an in-memory plan list (tests,
// tooling) with the same validation as Reload.
func NewPlansCacheFromPlans(plans []models.Plan) (*PlansCache, error) {
	c := &PlansCache{}
	if err := c.install(plans); err != nil {
		return nil, err
	}
	return c, nil
}

// Reload re-reads the collection and swaps both maps atomically under the
// write lock. A load that fails validation (duplicate price_id across docs)
// is REJECTED: the error is returned, the previous snapshot keeps serving.
func (c *PlansCache) Reload(ctx context.Context) error {
	plans, err := models.ListAllPlans(ctx)
	if err != nil {
		return err
	}
	return c.install(plans)
}

// install validates and swaps the snapshot; split from Reload so the
// swap/reject semantics are unit-testable without a DB.
func (c *PlansCache) install(plans []models.Plan) error {
	byPrice, byApp, err := buildPlanMaps(plans)
	if err != nil {
		log.Error("plans cache: reload rejected, keeping previous snapshot", "error", err)
		return err
	}
	c.mu.Lock()
	c.byPrice = byPrice
	c.byApp = byApp
	c.mu.Unlock()
	return nil
}

// StartRefresh runs Reload every interval until ctx is cancelled (shutdown).
// Failures are logged and retried next tick — the cache keeps serving the
// last good snapshot.
func (c *PlansCache) StartRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reloadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := c.Reload(reloadCtx); err != nil {
					log.Error("plans cache: background reload failed", "error", err)
				} else {
					c.mu.RLock()
					n := len(c.byPrice)
					c.mu.RUnlock()
					log.Info("plans cache: reloaded", "price_ids", n)
				}
				cancel()
			}
		}
	}()
}

// Accessors tolerate a nil receiver (cache not wired, e.g. in tests) by
// resolving nothing — the resolver then fails closed on features.

// ByPriceID resolves a Paddle price to its plan (feature lists).
func (c *PlansCache) ByPriceID(priceID string) (*models.Plan, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.byPrice[priceID]
	return p, ok
}

// ActivePaidPlanForApp returns the app's active (purchasable) plan — the tier
// an admin comp grants access "as-if-subscribed" to. Deterministic when the
// catalog somehow holds several active tiers (lowest tier string wins); the
// virtual trial_expired plan is inactive and never matches.
func (c *PlansCache) ActivePaidPlanForApp(app string) (*models.Plan, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var best *models.Plan
	for _, p := range c.byApp[app] {
		if !p.Active {
			continue
		}
		if best == nil || p.Tier < best.Tier {
			best = p
		}
	}
	return best, best != nil
}

// ByAppTier resolves a plan by its (app, tier) pair.
func (c *PlansCache) ByAppTier(app, tier string) (*models.Plan, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.byApp[app] {
		if p.Tier == tier {
			return p, true
		}
	}
	return nil, false
}

// AppForPrice is the webhook's app_id stamp: which app does this price sell?
// Fail closed on !ok — authorization scope must never be chosen by fallback.
func (c *PlansCache) AppForPrice(priceID string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.byPrice[priceID]; ok {
		return p.AppID, true
	}
	return "", false
}

// AppIDs enumerates the apps with plan docs (status endpoint union).
func (c *PlansCache) AppIDs() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.byApp))
	for app := range c.byApp {
		ids = append(ids, app)
	}
	sort.Strings(ids)
	return ids
}

// buildPlanMaps inverts the plan list into the two lookup maps, enforcing
// global price_id uniqueness — two plans claiming one price would make
// AppForPrice (an authorization input) ambiguous.
func buildPlanMaps(plans []models.Plan) (map[string]*models.Plan, map[string][]*models.Plan, error) {
	byPrice := map[string]*models.Plan{}
	byApp := map[string][]*models.Plan{}
	for i := range plans {
		p := &plans[i]
		for _, priceID := range p.PriceIDs() {
			if owner, dup := byPrice[priceID]; dup {
				return nil, nil, fmt.Errorf("duplicate price_id %s (%s/%s and %s/%s)",
					priceID, owner.AppID, owner.Tier, p.AppID, p.Tier)
			}
			byPrice[priceID] = p
		}
		byApp[p.AppID] = append(byApp[p.AppID], p)
	}
	return byPrice, byApp, nil
}
