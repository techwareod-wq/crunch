package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store defines the data-access methods for the payment service. The
// implementation delegates to the package-level functions in internal/models,
// where the payment documents and their queries live.
//
// Several methods bypass the models.UpdateOne helper deliberately: this
// design needs upserts, dup-key detection, and conditional updates where
// "no match" is the expected skip path — none of which the helper expresses.
type Store interface {
	FindCustomerByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.PaddleCustomer, error)
	FindCustomerByPaddleID(ctx context.Context, paddleCustomerID string) (bool, *models.PaddleCustomer, error)
	// InsertCustomer surfaces mongo duplicate-key errors unwrapped so callers
	// can recover from concurrent pre-create races.
	InsertCustomer(ctx context.Context, customer *models.PaddleCustomer) error
	// UpdateCustomerEmail refreshes the email on an existing mapping
	// (customer.updated webhook). Missing mapping is a no-op.
	UpdateCustomerEmail(ctx context.Context, paddleCustomerID, email string) error

	FindLatestSubscriptionByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.Subscription, error)
	FindLatestSubscriptionByUserIDForApp(ctx context.Context, userID primitive.ObjectID, appID string) (bool, *models.Subscription, error)
	FindSubscriptionByPaddleID(ctx context.Context, paddleSubscriptionID string) (bool, *models.Subscription, error)
	// FindSubscriptionsByUserID returns every subscription doc the user holds,
	// across all apps and statuses (admin deletion cascade).
	FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error)
	// IsSubscriptionTombstoned reports whether the admin deletion cascade
	// hard-deleted this subscription doc — the webhook apply path skips such
	// events instead of upserting the doc back to life.
	IsSubscriptionTombstoned(ctx context.Context, paddleSubscriptionID string) (bool, error)
	HasValidSubscription(ctx context.Context, userID primitive.ObjectID) (bool, error)
	// SetSubscriptionCanceling / SetSubscriptionActive are optimistic local
	// writes after a successful Paddle API call. They intentionally do NOT
	// advance last_event_at — the confirming subscription.updated webhook
	// carries the authoritative state.
	SetSubscriptionCanceling(ctx context.Context, paddleSubscriptionID string, scheduledCancelAt *time.Time) error
	SetSubscriptionActive(ctx context.Context, paddleSubscriptionID string) error
	// SetSubscriptionCanceled records an immediate cancellation and clamps
	// valid_till down to the cancellation moment.
	SetSubscriptionCanceled(ctx context.Context, paddleSubscriptionID string, canceledAt time.Time) error

	// Local-trial writes (source:"trial" docs — no Paddle object, no
	// confirming webhook; these writes ARE the authoritative state).
	// InsertTrialSubscription surfaces mongo duplicate-key errors unwrapped
	// (deterministic synthetic ID = one-trial-ever guard).
	InsertTrialSubscription(ctx context.Context, sub *models.Subscription) error
	// SetSubscriptionTrialing restores trialing status (local-trial un-cancel).
	SetSubscriptionTrialing(ctx context.Context, paddleSubscriptionID string) error
	// SetTrialValidTill moves a local trial's end (admin extend).
	SetTrialValidTill(ctx context.Context, paddleSubscriptionID string, validTill time.Time) error
	// EndLocalTrialsForUser terminates live local trials for (user, app) when
	// a real Paddle subscription supersedes them.
	EndLocalTrialsForUser(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error
	// IsWebEntityFinalised reports whether the user finished onboarding — the
	// card-less trial precondition.
	IsWebEntityFinalised(ctx context.Context, userID primitive.ObjectID) (bool, error)
	// GetLifetimeArticlesGenerated reads the monotonic generation counter of
	// the user's single web entity — the free-plan article meter surfaced on
	// /payments/status.
	GetLifetimeArticlesGenerated(ctx context.Context, userID primitive.ObjectID) (int, error)
	// ApplySubscriptionEvent atomically upserts subscription state from a
	// webhook event, guarded by last_event_at: the filter only matches when
	// the event is newer than the doc, and a stale event's upsert attempt
	// hits the unique paddle_subscription_id index instead. Returns
	// applied=false (no error) when the event was stale.
	ApplySubscriptionEvent(ctx context.Context, paddleSubscriptionID string, occurredAt time.Time, update bson.M) (applied bool, err error)
	// ApplyTransactionEvent is the same atomic guarded-upsert pattern keyed
	// on paddle_transaction_id (failed → completed reuses the same ID).
	ApplyTransactionEvent(ctx context.Context, paddleTransactionID string, occurredAt time.Time, update bson.M) (applied bool, err error)
	ListTransactionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Transaction, error)

	// RecomputeUserEntitlement projects (user, app) subscription state into
	// users.entitlements. INVARIANT: called after EVERY subscriptions write —
	// webhook applies (including applied==false redeliveries, the heal path)
	// and optimistic local writes.
	RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error
	// TrialEligibleIdentity reports whether the identity (live user,
	// tombstoned same-email users, same Paddle customer) has no prior
	// subscription history besides excludePaddleSubID. Flag-only input.
	TrialEligibleIdentity(ctx context.Context, userID primitive.ObjectID, paddleCustomerID, excludePaddleSubID string) (bool, error)

	// Trial lifecycle two-marker pattern: Claim* set the fact once
	// (filter-guarded, no-match = no-op); Mark*Dispatched stamp the
	// side-effect marker (best-effort — a lost stamp means one duplicate
	// dispatch, which idempotent handlers absorb).
	ClaimTrialConversion(ctx context.Context, paddleSubscriptionID string) error
	ClaimTrialExpiry(ctx context.Context, paddleSubscriptionID string) error
	MarkTrialConversionDispatched(ctx context.Context, paddleSubscriptionID string) error
	MarkTrialExpiryDispatched(ctx context.Context, paddleSubscriptionID string) error

	// Upgrade expand trigger (trial→paid): the webhook scans the user's
	// trial-mode WECs, claims each via the upgrade_state CAS, and dispatches
	// the expand pipeline. UnclaimWECUpgrade is the guarded rollback for a
	// claim whose dispatch failed (no-ops once the pipeline recorded progress).
	FindTrialModeWECsForUser(ctx context.Context, userID primitive.ObjectID) ([]models.WebEntityContext, error)
	ClaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) (bool, error)
	UnclaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) error

	// Company linkage (team seat billing). FindCompanyByID backs the
	// webhook's buyer-owns-company verification and the checkout guards;
	// FindLatestSubscriptionByCompanyID resolves the company's own sub
	// (doc-keyed on company_id, never the owner's user_id);
	// SetCompanyBilling syncs purchased seats from the subscription state;
	// ListClaimedMembershipUserIDs is the entitlement-recompute fan-out set.
	FindCompanyByID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Company, error)
	FindLatestSubscriptionByCompanyID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Subscription, error)
	SetCompanyBilling(ctx context.Context, companyID primitive.ObjectID, billing models.CompanyBilling) error
	ListClaimedMembershipUserIDs(ctx context.Context, companyID primitive.ObjectID) ([]primitive.ObjectID, error)

	// SIE cold-start backstop reads (see service.triggerSIEBackstop): resolve
	// the buyer's personal company, that company's web entity, and whether an
	// SIE run already exists for it.
	FindPersonalCompanyByOwner(ctx context.Context, ownerUserID primitive.ObjectID) (bool, *models.Company, error)
	FindWebEntityForCompany(ctx context.Context, companyID, fallbackUserID primitive.ObjectID) (bool, *models.WebEntity, error)
	FindWebEntityContextByWebEntityAndUser(ctx context.Context, webEntityID, userID primitive.ObjectID) (bool, *models.WebEntityContext, error)

	// InsertEvent inserts the event row. On a duplicate event_id it returns
	// duplicate=true plus the existing row so the caller can apply the
	// refined dedupe rule (skip only if already processed).
	InsertEvent(ctx context.Context, event *models.PaddleEvent) (duplicate bool, existing *models.PaddleEvent, err error)
	// MarkEventProcessed stamps processed_at; note records a non-retryable
	// resolution problem (e.g. unresolvable user) without triggering retries.
	MarkEventProcessed(ctx context.Context, eventID string, note string) error
	// MarkEventFailed records a transient processing error WITHOUT setting
	// processed_at, so the Paddle redelivery is reprocessed.
	MarkEventFailed(ctx context.Context, eventID string, errMsg string) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) FindCustomerByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
	return models.FindCustomerByUserID(ctx, userID)
}

func (s *store) FindCustomerByPaddleID(ctx context.Context, paddleCustomerID string) (bool, *models.PaddleCustomer, error) {
	return models.FindCustomerByPaddleID(ctx, paddleCustomerID)
}

func (s *store) InsertCustomer(ctx context.Context, customer *models.PaddleCustomer) error {
	return models.InsertCustomer(ctx, customer)
}

func (s *store) UpdateCustomerEmail(ctx context.Context, paddleCustomerID, email string) error {
	return models.UpdateCustomerEmail(ctx, paddleCustomerID, email)
}

func (s *store) FindLatestSubscriptionByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.Subscription, error) {
	return models.FindLatestSubscriptionByUserID(ctx, userID)
}

func (s *store) FindLatestSubscriptionByUserIDForApp(ctx context.Context, userID primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
	return models.FindLatestSubscriptionByUserIDForApp(ctx, userID, appID)
}

func (s *store) FindSubscriptionByPaddleID(ctx context.Context, paddleSubscriptionID string) (bool, *models.Subscription, error) {
	return models.FindSubscriptionByPaddleID(ctx, paddleSubscriptionID)
}

func (s *store) FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error) {
	return models.FindSubscriptionsByUserID(ctx, userID)
}

func (s *store) IsSubscriptionTombstoned(ctx context.Context, paddleSubscriptionID string) (bool, error) {
	return models.IsSubscriptionTombstoned(ctx, paddleSubscriptionID)
}

func (s *store) HasValidSubscription(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	return models.HasValidSubscription(ctx, userID)
}

func (s *store) SetSubscriptionCanceling(ctx context.Context, paddleSubscriptionID string, scheduledCancelAt *time.Time) error {
	return models.SetSubscriptionCanceling(ctx, paddleSubscriptionID, scheduledCancelAt)
}

func (s *store) SetSubscriptionActive(ctx context.Context, paddleSubscriptionID string) error {
	return models.SetSubscriptionActive(ctx, paddleSubscriptionID)
}

func (s *store) SetSubscriptionCanceled(ctx context.Context, paddleSubscriptionID string, canceledAt time.Time) error {
	return models.SetSubscriptionCanceled(ctx, paddleSubscriptionID, canceledAt)
}

func (s *store) InsertTrialSubscription(ctx context.Context, sub *models.Subscription) error {
	return models.InsertTrialSubscription(ctx, sub)
}

func (s *store) SetSubscriptionTrialing(ctx context.Context, paddleSubscriptionID string) error {
	return models.SetSubscriptionTrialing(ctx, paddleSubscriptionID)
}

func (s *store) SetTrialValidTill(ctx context.Context, paddleSubscriptionID string, validTill time.Time) error {
	return models.SetTrialValidTill(ctx, paddleSubscriptionID, validTill)
}

func (s *store) EndLocalTrialsForUser(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error {
	return models.EndLocalTrialsForUser(ctx, userID, appID, at)
}

func (s *store) GetLifetimeArticlesGenerated(ctx context.Context, userID primitive.ObjectID) (int, error) {
	return models.GetLifetimeArticlesGeneratedForUser(ctx, userID)
}

func (s *store) IsWebEntityFinalised(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	return models.IsWebEntityFinalised(ctx, userID)
}

func (s *store) ApplySubscriptionEvent(ctx context.Context, paddleSubscriptionID string, occurredAt time.Time, update bson.M) (bool, error) {
	return models.ApplySubscriptionEvent(ctx, paddleSubscriptionID, occurredAt, update)
}

func (s *store) ApplyTransactionEvent(ctx context.Context, paddleTransactionID string, occurredAt time.Time, update bson.M) (bool, error) {
	return models.ApplyTransactionEvent(ctx, paddleTransactionID, occurredAt, update)
}

func (s *store) ListTransactionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Transaction, error) {
	return models.ListTransactionsByUserID(ctx, userID)
}

func (s *store) RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error {
	return models.RecomputeUserEntitlement(ctx, userID, appID)
}

func (s *store) TrialEligibleIdentity(ctx context.Context, userID primitive.ObjectID, paddleCustomerID, excludePaddleSubID string) (bool, error) {
	return models.TrialEligibleIdentity(ctx, userID, paddleCustomerID, excludePaddleSubID)
}

func (s *store) ClaimTrialConversion(ctx context.Context, paddleSubscriptionID string) error {
	return models.ClaimTrialConversion(ctx, paddleSubscriptionID)
}

func (s *store) ClaimTrialExpiry(ctx context.Context, paddleSubscriptionID string) error {
	return models.ClaimTrialExpiry(ctx, paddleSubscriptionID)
}

func (s *store) MarkTrialConversionDispatched(ctx context.Context, paddleSubscriptionID string) error {
	return models.MarkTrialConversionDispatched(ctx, paddleSubscriptionID)
}

func (s *store) MarkTrialExpiryDispatched(ctx context.Context, paddleSubscriptionID string) error {
	return models.MarkTrialExpiryDispatched(ctx, paddleSubscriptionID)
}

func (s *store) FindTrialModeWECsForUser(ctx context.Context, userID primitive.ObjectID) ([]models.WebEntityContext, error) {
	return models.FindTrialModeWECsForUser(ctx, userID)
}

func (s *store) ClaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) (bool, error) {
	return models.ClaimWECUpgrade(ctx, wecID)
}

func (s *store) UnclaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) error {
	return models.UnclaimWECUpgrade(ctx, wecID)
}

func (s *store) FindCompanyByID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Company, error) {
	return models.FindCompanyByID(ctx, companyID)
}

func (s *store) FindLatestSubscriptionByCompanyID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Subscription, error) {
	return models.FindLatestSubscriptionByCompanyID(ctx, companyID)
}

func (s *store) SetCompanyBilling(ctx context.Context, companyID primitive.ObjectID, billing models.CompanyBilling) error {
	return models.SetCompanyBilling(ctx, companyID, billing)
}

func (s *store) FindPersonalCompanyByOwner(ctx context.Context, ownerUserID primitive.ObjectID) (bool, *models.Company, error) {
	return models.FindPersonalCompanyByOwner(ctx, ownerUserID)
}

func (s *store) FindWebEntityForCompany(ctx context.Context, companyID, fallbackUserID primitive.ObjectID) (bool, *models.WebEntity, error) {
	return models.FindWebEntityForCompany(ctx, companyID, fallbackUserID)
}

// FindWebEntityContextByWebEntityAndUser adapts the hex-string models helper.
// The {web_entity_id, user_id} pair is the same key Orchestrate reads, so a hit
// here means Orchestrate would find a WEC too.
func (s *store) FindWebEntityContextByWebEntityAndUser(ctx context.Context, webEntityID, userID primitive.ObjectID) (bool, *models.WebEntityContext, error) {
	return models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityID.Hex(), userID.Hex())
}

func (s *store) ListClaimedMembershipUserIDs(ctx context.Context, companyID primitive.ObjectID) ([]primitive.ObjectID, error) {
	return models.ListClaimedMembershipUserIDs(ctx, companyID)
}

func (s *store) InsertEvent(ctx context.Context, event *models.PaddleEvent) (bool, *models.PaddleEvent, error) {
	return models.InsertEvent(ctx, event)
}

func (s *store) MarkEventProcessed(ctx context.Context, eventID string, note string) error {
	return models.MarkEventProcessed(ctx, eventID, note)
}

func (s *store) MarkEventFailed(ctx context.Context, eventID string, errMsg string) error {
	return models.MarkEventFailed(ctx, eventID, errMsg)
}
