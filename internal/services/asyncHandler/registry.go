package asyncHandler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
)

// Registry maps each process type to the handler that runs it.
type Registry map[pipeline.ProcessType]ProcessHandler

// ServiceLocator carries the services whose async work this registry routes.
type ServiceLocator struct {
	AttributeService attributeService.AttributeService
	CatalogService   catalogService.CatalogService
}

// BuildProcessRegistry binds every process type to its service handler.
func BuildProcessRegistry(locator *ServiceLocator) Registry {
	registry := NewRegistry()
	registerAttributeHandlers(registry, locator)
	registerCatalogHandlers(registry, locator)
	return registry
}

func registerAttributeHandlers(registry Registry, locator *ServiceLocator) {
	svc := locator.AttributeService
	registry.Register(attributeService.ProcessRecomputeAll, Typed(func(ctx context.Context, _ string, p attributeService.RecomputeAllPayload) error {
		return svc.RecomputeAll(ctx, p)
	}))
	registry.Register(attributeService.ProcessRecomputeBatch, Typed(func(ctx context.Context, _ string, p attributeService.RecomputeBatchPayload) error {
		return svc.RecomputeBatch(ctx, p)
	}))
}

func registerCatalogHandlers(registry Registry, locator *ServiceLocator) {
	svc := locator.CatalogService
	registry.Register(catalogService.ProcessGeocodeRetry, Typed(func(ctx context.Context, _ string, p catalogService.GeocodeRetryPayload) error {
		return svc.GeocodeRetry(ctx, p)
	}))
	registry.Register(catalogService.ProcessMediaGC, Typed(func(ctx context.Context, _ string, _ struct{}) error {
		return svc.MediaGC(ctx)
	}))
}

// NewRegistry returns an empty registry.
func NewRegistry() Registry {
	return Registry{}
}

// Register binds a handler to a process type. A duplicate registration is a
// boot-time wiring bug (two services claiming one process type), so it panics
// rather than letting the later registration silently win.
func (r Registry) Register(pt pipeline.ProcessType, h ProcessHandler) {
	if _, dup := r[pt]; dup {
		panic(fmt.Sprintf("asyncHandler: process type %q registered twice", pt))
	}
	r[pt] = h
}

// Typed adapts a strongly-typed handler into a ProcessHandler: it decodes the
// message payload into T and passes the envelope's user ID through. A payload
// that doesn't decode can never succeed on retry, so it is marked permanent.
func Typed[T any](fn func(ctx context.Context, userID string, p T) error) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p T
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal %s payload: %w: %w", msg.ProcessType, err, pipeline.ErrPermanent)
		}
		return fn(ctx, msg.UserID, p)
	}
}
