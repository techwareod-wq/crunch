package paymentService

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

// PaymentService is the app-facing contract for subscriptions and billing.
// All reads come from Mongo (webhooks are the writer); only checkout, cancel
// and resubscribe call the Paddle API.
type PaymentService interface {
	// GetStatus builds the legacy subscription shape (store read) plus the
	// per-app Apps map from the caller-supplied user's entitlement projection
	// — pass the request-context user; no extra store read happens for Apps.
	GetStatus(ctx context.Context, user *models.User) (*dto.PaymentStatusResponse, error)
	// GetPlans returns the plans to offer this user. The trial price is only
	// offered to trial-eligible users (no prior subscription); returning users
	// get the no-trial price so the free trial is never granted twice.
	GetPlans(ctx context.Context, userID primitive.ObjectID) ([]dto.PlanDTO, error)
	CreateCheckoutSession(ctx context.Context, userID primitive.ObjectID, email string) (*dto.CheckoutSessionResponse, error)
	// StartTrial provisions a card-less local trial for the plan owning
	// priceID (plan.trial_days > 0 required): a source:"trial" subscription
	// doc, no Paddle objects, no payment method. One trial ever per identity.
	StartTrial(ctx context.Context, userID primitive.ObjectID, priceID string) (*dto.StartTrialResponse, error)
	CancelSubscription(ctx context.Context, userID primitive.ObjectID) (*dto.CancelResponse, error)
	// CancelSubscriptionImmediately terminates the subscription now rather than
	// at period end, for account deletion (Clerk user.deleted). Idempotent: a
	// missing or already-canceled subscription is a nil-return no-op, so the
	// Clerk webhook can safely retry.
	CancelSubscriptionImmediately(ctx context.Context, userID primitive.ObjectID) error
	// CancelAllSubscriptionsImmediately cancels EVERY non-canceled subscription
	// the user holds (paid, Paddle trial, card-less local trial) with cancel-now
	// semantics, across all apps. Idempotent — already-canceled docs are
	// skipped, so a failed run can simply be retried. Used by the admin
	// deletion cascade before it hard-deletes the subscription docs.
	CancelAllSubscriptionsImmediately(ctx context.Context, userID primitive.ObjectID) error
	Resubscribe(ctx context.Context, userID primitive.ObjectID) (*dto.ResubscribeResponse, error)
	ListTransactions(ctx context.Context, userID primitive.ObjectID) ([]dto.TransactionDTO, error)
	// HandleWebhookEvent processes a verified Paddle webhook: insert-first
	// dedupe, type dispatch, out-of-order guard. A nil return means "ack 200"
	// (including duplicates and permanently unresolvable events); an error
	// means "respond 500 so Paddle redelivers".
	HandleWebhookEvent(ctx context.Context, envelope dto.WebhookEnvelope, rawBody []byte) error
	// HasValidSubscription is the middleware fast path: one indexed Mongo read.
	HasValidSubscription(ctx context.Context, userID primitive.ObjectID) (bool, error)

	// Company (team seat) billing. One Paddle subscription on the company;
	// line-item quantity = purchased seats. All four are fully operable with
	// no Paddle team product configured: checkout 400s first (the dark-ship
	// enforcement point), GetCompanyBilling never calls Paddle, cancel/resume
	// 404 cleanly when no company-linked subscription exists.
	CreateCompanyCheckoutSession(ctx context.Context, userID primitive.ObjectID, email string, companyID primitive.ObjectID) (*dto.CompanyCheckoutSessionResponse, error)
	GetCompanyBilling(ctx context.Context, companyID primitive.ObjectID) (*dto.CompanyBillingResponse, error)
	CancelCompanySubscription(ctx context.Context, userID, companyID primitive.ObjectID) (*dto.CancelResponse, error)
	ResumeCompanySubscription(ctx context.Context, userID, companyID primitive.ObjectID) (*dto.ResubscribeResponse, error)
	// ExtendTrial pushes a live trial's next billing date out by extendDays
	// (admin support op). Returns the new trial end; the webhook confirms.
	ExtendTrial(ctx context.Context, userID primitive.ObjectID, appID string, extendDays int) (*time.Time, error)
	// EndTrial terminates a live trial now: mode "convert" bills immediately,
	// mode "cancel" ends it with no charge (admin support op).
	EndTrial(ctx context.Context, userID primitive.ObjectID, appID, mode string) error
}
