package clerkaccounts

import (
	"errors"
	"fmt"
	"testing"

	"github.com/clerk/clerk-sdk-go/v2"
)

// The cascade's idempotency depends on "already deleted at Clerk" being
// success: only a genuine 404 API response qualifies — wrapped or not — and
// nothing else.
func TestIsNotFound(t *testing.T) {
	notFound := &clerk.APIErrorResponse{HTTPStatusCode: 404}
	serverErr := &clerk.APIErrorResponse{HTTPStatusCode: 500}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"clerk 404", notFound, true},
		{"wrapped clerk 404", fmt.Errorf("call: %w", notFound), true},
		{"clerk 500", serverErr, false},
		{"generic error", errors.New("network down"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Errorf("isNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
