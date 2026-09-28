package interfaces

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
)

// ErrPaddleCustomerExists is returned by CreateCustomer when Paddle already
// knows the email (Mongo/Paddle drift, manual dashboard creation). Callers
// recover via GetCustomerByEmail instead of failing checkout.
var ErrPaddleCustomerExists = errors.New("paddle customer already exists for this email")

// PaddleClient is the app-facing contract for the Paddle Billing API.
// Only the operations the payment service actually consumes are exposed;
// the SDK never leaks outside the impl package.
type PaddleClient interface {
	CreateCustomer(ctx context.Context, email string, customData map[string]any) (*dto.PaddleCustomer, error)
	// GetCustomerByEmail resolves an existing customer when CreateCustomer
	// fails with customer_already_exists (Mongo/Paddle drift). Returns the
	// customer with Archived=true if the Paddle record is archived.
	GetCustomerByEmail(ctx context.Context, email string) (*dto.PaddleCustomer, error)
	// ReactivateCustomer sets an archived Paddle customer back to active so
	// they can open a new checkout. No-op if they are already active.
	ReactivateCustomer(ctx context.Context, customerID string) error
	// ListActivePrices returns active recurring prices for the product,
	// served from a short-TTL in-memory cache.
	ListActivePrices(ctx context.Context, productID string) ([]dto.PaddlePrice, error)
	GetSubscription(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error)
	// ListLiveSubscriptionsByCustomer returns the customer's subscriptions in
	// active / trialing / past_due states straight from Paddle. Used to close
	// the webhook-lag window where a paid subscription has no Mongo doc yet
	// and a second checkout would double-bill.
	ListLiveSubscriptionsByCustomer(ctx context.Context, customerID string) ([]dto.PaddleSubscription, error)
	// CancelAtPeriodEnd schedules cancellation for the end of the current billing period.
	CancelAtPeriodEnd(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error)
	// CancelImmediately terminates the subscription now, with no paid-through
	// period. Used when the account itself is gone (Clerk user.deleted), where
	// there is nothing left to honour the remaining period for.
	CancelImmediately(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error)
	// RemoveScheduledCancellation un-cancels a subscription that has a pending scheduled change.
	RemoveScheduledCancellation(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error)
	// SetNextBilledAt moves the subscription's next billing date — used to
	// extend a trial (the trial runs until next_billed_at). The confirming
	// subscription.updated webhook carries the authoritative state.
	SetNextBilledAt(ctx context.Context, subscriptionID string, at time.Time) (*dto.PaddleSubscription, error)
	// ActivateTrialingSubscription bills a trialing subscription immediately,
	// converting the trial to paid (admin "end trial as convert").
	ActivateTrialingSubscription(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error)
	// VerifyWebhook validates the Paddle-Signature header against the raw body.
	// Returns a non-nil error for both transport problems and signature mismatch.
	VerifyWebhook(req *http.Request, rawBody []byte) error
}
