package asyncHandler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/pipeline"
)

// Registry maps each process type to the handler that runs it. It carries no
// feature knowledge: every feature module registers its own handlers at boot
// (see internal/modules), so adding a feature never edits this package.
type Registry map[pipeline.ProcessType]ProcessHandler

// NewRegistry returns an empty registry.
func NewRegistry() Registry {
	return Registry{}
}

// Register binds a handler to a process type. A duplicate registration is a
// boot-time wiring bug (two modules claiming one process type), so it panics
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
