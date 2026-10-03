package service

import (
	"context"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// reloadTimeout bounds one background snapshot reload.
const reloadTimeout = 10 * time.Second

// Cache holds the current rules snapshot: write-through
// Reload after every tree/industry write on the instance that served it, plus
// a StartRefresh ticker that bounds staleness on the others.
type Cache struct {
	load func(ctx context.Context) (*domain.Snapshot, error)

	mu   sync.RWMutex
	snap *domain.Snapshot
}

var _ domain.Rules = (*Cache)(nil)

// NewCache returns a cache serving the empty snapshot until the first Reload.
func NewCache(load func(ctx context.Context) (*domain.Snapshot, error)) *Cache {
	return &Cache{load: load, snap: domain.EmptySnapshot()}
}

// Snapshot returns the current snapshot (never nil).
func (c *Cache) Snapshot() *domain.Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

// Reload reads the tree and swaps the snapshot in. A load that comes back
// older than what is cached (a slow read racing a newer reload) is dropped.
func (c *Cache) Reload(ctx context.Context) error {
	s, err := c.load(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if s.Version >= c.snap.Version {
		c.snap = s
	}
	c.mu.Unlock()
	return nil
}

// SnapshotAtLeast returns a snapshot at version ≥ v, reloading once when the
// cached one is older. Still older after the reload (the bump hasn't landed
// on this replica) is reported as an error so async callers retry.
func (c *Cache) SnapshotAtLeast(ctx context.Context, v int64) (*domain.Snapshot, error) {
	if s := c.Snapshot(); s.Version >= v {
		return s, nil
	}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	s := c.Snapshot()
	if s.Version < v {
		return nil, &attributeService.StaleSnapshotError{Have: s.Version, Want: v}
	}
	return s, nil
}

// StartRefresh reloads every interval until ctx is cancelled. Failures are
// logged; the last good snapshot keeps serving.
func (c *Cache) StartRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rctx, cancel := context.WithTimeout(ctx, reloadTimeout)
				if err := c.Reload(rctx); err != nil {
					log.Error("attributes cache: background reload failed", "error", err)
				}
				cancel()
			}
		}
	}()
}
