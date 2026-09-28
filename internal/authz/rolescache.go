package authz

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// reloadTimeout bounds each background catalog reload.
const reloadTimeout = 10 * time.Second

// RolesCache is the in-memory role catalog, mirroring entitlements.PlansCache:
// write-through Reload on every catalog write PLUS a background ticker that
// bounds staleness for a process that didn't serve the write (rolling deploys
// briefly run two). Because jwt.go already loads the full user doc on every
// authenticated request, resolving permissions against this cache costs zero
// extra DB reads.
type RolesCache struct {
	mu    sync.RWMutex
	byKey map[string]*models.Role
}

// NewRolesCache boot-loads the catalog. A DB error hard-fails startup (a
// service that can't resolve roles would 403 the entire admin surface), but an
// EMPTY catalog is tolerated and logged — the binary deploys before the seed
// run, and a warn keeps a post-cutover lockout diagnosable rather than silent
// (RBAC plan §9.1).
func NewRolesCache(ctx context.Context) (*RolesCache, error) {
	c := &RolesCache{byKey: map[string]*models.Role{}}
	if err := c.Reload(ctx); err != nil {
		return nil, fmt.Errorf("roles cache boot load: %w", err)
	}
	c.mu.RLock()
	n := len(c.byKey)
	c.mu.RUnlock()
	if n == 0 {
		log.Warn("roles catalog empty — admin surface disabled until rolesmigrate -seed-roles runs")
	}
	return c, nil
}

// NewRolesCacheFromRoles builds a cache from an in-memory role list (tests,
// tooling) with the same validation as Reload.
func NewRolesCacheFromRoles(roles []models.Role) (*RolesCache, error) {
	c := &RolesCache{}
	if err := c.install(roles); err != nil {
		return nil, err
	}
	return c, nil
}

// Reload re-reads the collection and swaps the map atomically under the write
// lock. A load that fails validation (duplicate key, unknown permission) is
// REJECTED: the error is returned and the previous snapshot keeps serving.
func (c *RolesCache) Reload(ctx context.Context) error {
	roles, err := models.ListAllRoles(ctx)
	if err != nil {
		return err
	}
	return c.install(roles)
}

// install validates and swaps the snapshot; split from Reload so the
// swap/reject semantics are unit-testable without a DB.
func (c *RolesCache) install(roles []models.Role) error {
	byKey, err := buildRoleMap(roles)
	if err != nil {
		log.Error("roles cache: reload rejected, keeping previous snapshot", "error", err)
		return err
	}
	c.mu.Lock()
	c.byKey = byKey
	c.mu.Unlock()
	return nil
}

// StartRefresh runs Reload every interval until ctx is cancelled (shutdown).
// Failures are logged and retried next tick — the cache keeps serving the last
// good snapshot. Mirrors PlansCache.StartRefresh.
func (c *RolesCache) StartRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reloadCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
				if err := c.Reload(reloadCtx); err != nil {
					log.Error("roles cache: background reload failed", "error", err)
				} else {
					c.mu.RLock()
					n := len(c.byKey)
					c.mu.RUnlock()
					log.Info("roles cache: reloaded", "roles", n)
				}
				cancel()
			}
		}
	}()
}

// ByKey resolves a role key to its doc. Tolerates a nil receiver (cache not
// wired, e.g. tests) by resolving nothing — the resolver then fails closed.
func (c *RolesCache) ByKey(key string) (*models.Role, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.byKey[key]
	return r, ok
}

// Keys enumerates the role keys in the catalog (sorted; diagnostics/tests).
func (c *RolesCache) Keys() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.byKey))
	for k := range c.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildRoleMap inverts the role list into the key→role map, enforcing key
// uniqueness (a duplicate key would make ByKey ambiguous) and permission
// validity. COMPANY roles (user_admin/user_user, tenancy plan D17) validate
// against the company registry — company keys only, no wildcard, no global
// keys — while every other role validates against the global registry, which
// rejects company keys. Either failure rejects the whole snapshot so a bad
// catalog never starts serving.
func buildRoleMap(roles []models.Role) (map[string]*models.Role, error) {
	byKey := make(map[string]*models.Role, len(roles))
	for i := range roles {
		r := &roles[i]
		if r.Key == "" {
			return nil, fmt.Errorf("role with empty key")
		}
		if _, dup := byKey[r.Key]; dup {
			return nil, fmt.Errorf("duplicate role key %q", r.Key)
		}
		valid := IsValidRolePermission
		if IsCompanyRoleKey(r.Key) {
			valid = IsValidCompanyRolePermission
		}
		for _, p := range r.Permissions {
			if !valid(p) {
				return nil, fmt.Errorf("role %q has unknown permission %q", r.Key, p)
			}
		}
		byKey[r.Key] = r
	}
	return byKey, nil
}
