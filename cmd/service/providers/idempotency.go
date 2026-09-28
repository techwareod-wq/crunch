package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/providers/impl/idempotency"
)

// InjectDefaultIdempotencyStore wires the dedupe store the async handler uses to
// drop duplicate at-least-once deliveries. Backend swap point: replace
// NewMongoStore with a Redis (or other) constructor satisfying the same
// interfaces.IdempotencyStore contract — nothing else changes.
//
// The dedupe-row TTL (values.idempotency.ttlHours) must exceed the longest
// window over which a message can be redelivered (max SQS retention of 14 days
// plus retry churn), so a late redelivery still finds its record.
func InjectDefaultIdempotencyStore(appCtx *config.AppContext) error {
	ttl := time.Duration(appCtx.Config.Values.Idempotency.TTLHours) * time.Hour
	store, err := idempotency.NewMongoStore(context.Background(), ttl)
	if err != nil {
		return fmt.Errorf("init idempotency store: %w", err)
	}
	appCtx.IdempotencyStore = store
	return nil
}
