package asyncHandler

import "context"

// ProcessHandler is the function signature that all async process handlers must implement.
type ProcessHandler func(ctx context.Context, msg MessageEnvelope) error
