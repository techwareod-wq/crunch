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
	// CreateInvitation sends a Clerk sign-up invitation to email with the given
	// public metadata (e.g. {"whRole": "editor"}). redirectURL may be empty
	// (Clerk's default). expiresInDays 0 keeps Clerk's default. Returns the
	// Clerk invitation id.
	CreateInvitation(ctx context.Context, email string, publicMetadata map[string]any, redirectURL string, expiresInDays int) (string, error)
	// RevokeInvitation revokes a pending Clerk invitation. A Clerk 404 is
	// success (already gone).
	RevokeInvitation(ctx context.Context, invitationID string) error
}
