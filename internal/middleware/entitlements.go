package middleware

import (
	"github.com/atharva-ng/crunch/internal/entitlements"
)

// plansCache is the boot-injected plan catalog the entitlement middlewares
// resolve against. Configured once at startup via SetPlansCache — mirrors
// SetRolesCache. Consumed by WithAppSubscription / WithFeature.
var plansCache *entitlements.PlansCache

// SetPlansCache injects the plans cache. Call once at startup, before
// serving.
func SetPlansCache(c *entitlements.PlansCache) {
	plansCache = c
}
