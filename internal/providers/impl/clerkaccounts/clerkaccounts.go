package clerkaccounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/clerk/clerk-sdk-go/v2"
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

// isNotFound reports whether the SDK error is a Clerk 404.
func isNotFound(err error) bool {
	var apiErr *clerk.APIErrorResponse
	return errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusNotFound
}
