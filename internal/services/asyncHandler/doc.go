// Package asyncHandler is an intentionally flat infrastructure dispatcher: it
// owns SQS message routing and consumer plumbing (receive, idempotency gating,
// visibility extension, retry/DLQ handling, acknowledgement), delegating the
// actual work to registered process handlers.
//
// It is deliberately NOT structured as a layered domain engine like the other
// services (repository/service/handler tiers). There is no domain model here —
// only transport-level dispatch — so a single flat package is the right shape.
package asyncHandler
