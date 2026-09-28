package interfaces

import "context"

// IdempotencyStore deduplicates at-least-once message delivery. It is keyed on a
// stable message ID that survives both SQS redelivery (identical body) and the
// re-enqueue retries in the async handler (same MessageID is re-published).
//
// This is the only contract the async handler depends on, so the backing store
// is swappable: the Mongo implementation can be replaced by Redis (or anything
// else) by satisfying this interface and changing one line in the injector.
type IdempotencyStore interface {
	// BeginProcessing records the first sighting of msgID and reports whether a
	// prior delivery already COMPLETED it.
	//
	//   alreadyProcessed == true  → a previous run finished; caller skips + acks.
	//   alreadyProcessed == false → caller should process. This covers both a
	//                               fresh message and a prior attempt that did
	//                               not complete (a genuine retry).
	BeginProcessing(ctx context.Context, msgID string) (alreadyProcessed bool, err error)

	// MarkProcessed records msgID as completed so future deliveries are skipped.
	// Must be called AFTER the handler succeeds and BEFORE the queue ack, so a
	// crash in the gap is absorbed by the next delivery being skipped.
	MarkProcessed(ctx context.Context, msgID string) error
}
