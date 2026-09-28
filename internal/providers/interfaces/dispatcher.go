package interfaces

import "context"

// Dispatcher enqueues async work onto the message queue.
type Dispatcher interface {
	Dispatch(ctx context.Context, processType string, userID string, payload any) error

	// DispatchKeyed enqueues like Dispatch but with a caller-chosen stable
	// message ID instead of a fresh UUID. Because the async handler's
	// idempotency gate dedupes on MessageID, two independent DispatchKeyed
	// calls with the same key collapse into one handler run — the cron layer's
	// unit-level exactly-once (a claim takeover re-dispatching an occurrence's
	// units must not re-run the ones the dead node already delivered).
	DispatchKeyed(ctx context.Context, processType string, userID string, messageID string, payload any) error
}
