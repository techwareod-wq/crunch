package clerkaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/invitation"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// provider calls the Clerk Backend API through the SDK's package-level
// functions, which authenticate via the global clerk.SetKey done at boot
// (cmd/service/app_context.go).
type provider struct{}

func GetProvider() interfaces.ClerkAccounts {
	return &provider{}
}

func (p *provider) DeleteUser(ctx context.Context, clerkID string) error {
	_, err := clerkuser.Delete(ctx, clerkID)
	if err == nil || isNotFound(err) {
		// 404 = already deleted — idempotent success for cascade re-runs.
		return nil
	}
	return fmt.Errorf("clerk delete user %s: %w", clerkID, err)
}

func (p *provider) CreateInvitation(ctx context.Context, email string, publicMetadata map[string]any, redirectURL string, expiresInDays int) (string, error) {
	params := &invitation.CreateParams{EmailAddress: email}
	if len(publicMetadata) > 0 {
		raw, err := json.Marshal(publicMetadata)
		if err != nil {
			return "", fmt.Errorf("clerk invitation metadata: %w", err)
		}
		msg := json.RawMessage(raw)
		params.PublicMetadata = &msg
	}
	if redirectURL != "" {
		params.RedirectURL = &redirectURL
	}
	if expiresInDays > 0 {
		days := int64(expiresInDays)
		params.ExpiresInDays = &days
	}
	inv, err := invitation.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("clerk create invitation: %w", err)
	}
	return inv.ID, nil
}

func (p *provider) RevokeInvitation(ctx context.Context, invitationID string) error {
	_, err := invitation.Revoke(ctx, invitationID)
	if err == nil || isNotFound(err) {
		return nil
	}
	return fmt.Errorf("clerk revoke invitation %s: %w", invitationID, err)
}

// isNotFound reports whether the SDK error is a Clerk 404.
func isNotFound(err error) bool {
	var apiErr *clerk.APIErrorResponse
	return errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusNotFound
}
