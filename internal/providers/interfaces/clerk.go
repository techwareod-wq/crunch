package interfaces

import "context"

// ClerkAccounts is the outbound Clerk Backend API surface (account lifecycle).
// Inbound Clerk traffic (JWT verification, webhooks) does not go through this
// interface — it exists so services that destroy accounts can be tested
// without the SDK's global client.
type ClerkAccounts interface {
	// DeleteUser deletes the Clerk user, revoking authentication everywhere.
	// A Clerk 404 is success (already deleted) so cascade re-runs converge.
	// Deleting triggers Clerk's user.deleted webhook, whose handler must stay
	// idempotent alongside the admin deletion cascade.
	DeleteUser(ctx context.Context, clerkID string) error
}
