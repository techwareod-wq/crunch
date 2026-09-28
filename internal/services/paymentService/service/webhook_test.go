package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/services/paymentService/store"
)

// mockStore implements store.Store with overridable funcs. Any method called
// without an override panics, so tests fail loudly on unexpected calls.
type mockStore struct {
	insertEvent        func(ctx context.Context, event *models.PaddleEvent) (bool, *models.PaddleEvent, error)
	markProcessed      func(ctx context.Context, eventID, note string) error
	markFailed         func(ctx context.Context, eventID, errMsg string) error
	findCustomer       func(ctx context.Context, paddleCustomerID string) (bool, *models.PaddleCustomer, error)
	updateEmail        func(ctx context.Context, paddleCustomerID, email string) error
	insertCustomer     func(ctx context.Context, customer *models.PaddleCustomer) error
	findSubByPaddleID  func(ctx context.Context, id string) (bool, *models.Subscription, error)
	findLatestByUser   func(ctx context.Context, userID primitive.ObjectID) (bool, *models.Subscription, error)
	findLatestForApp   func(ctx context.Context, userID primitive.ObjectID, appID string) (bool, *models.Subscription, error)
	setActive          func(ctx context.Context, paddleSubscriptionID string) error
	applySubscription  func(ctx context.Context, id string, occurredAt time.Time, update bson.M) (bool, error)
	applyTransaction   func(ctx context.Context, id string, occurredAt time.Time, update bson.M) (bool, error)
	setCanceled        func(ctx context.Context, paddleSubscriptionID string, canceledAt time.Time) error
	recompute          func(ctx context.Context, userID primitive.ObjectID, appID string) error
	trialEligible      func(ctx context.Context, userID primitive.ObjectID, paddleCustomerID, excludePaddleSubID string) (bool, error)
	claimConversion    func(ctx context.Context, paddleSubscriptionID string) error
	claimExpiry        func(ctx context.Context, paddleSubscriptionID string) error
	markConvDispatch   func(ctx context.Context, paddleSubscriptionID string) error
	markExpDispatch    func(ctx context.Context, paddleSubscriptionID string) error
	insertTrialSub     func(ctx context.Context, sub *models.Subscription) error
	setTrialing        func(ctx context.Context, paddleSubscriptionID string) error
	setTrialValidTill  func(ctx context.Context, paddleSubscriptionID string, validTill time.Time) error
	endLocalTrials     func(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error
	findCustomerByUser func(ctx context.Context, userID primitive.ObjectID) (bool, *models.PaddleCustomer, error)
	webEntityFinalised func(ctx context.Context, userID primitive.ObjectID) (bool, error)
	setCanceling       func(ctx context.Context, paddleSubscriptionID string, scheduledCancelAt *time.Time) error
	findTrialWECs      func(ctx context.Context, userID primitive.ObjectID) ([]models.WebEntityContext, error)
	claimWECUpgrade    func(ctx context.Context, wecID primitive.ObjectID) (bool, error)
	unclaimWECUpgrade  func(ctx context.Context, wecID primitive.ObjectID) error
	findSubsByUser     func(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error)
	isSubTombstoned    func(ctx context.Context, paddleSubscriptionID string) (bool, error)
	lifetimeArticles   func(ctx context.Context, userID primitive.ObjectID) (int, error)
	findCompanyByID    func(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Company, error)
	findLatestByComp   func(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Subscription, error)
	setCompanyBilling  func(ctx context.Context, companyID primitive.ObjectID, billing models.CompanyBilling) error
	listClaimedUserIDs func(ctx context.Context, companyID primitive.ObjectID) ([]primitive.ObjectID, error)
	findPersonalComp   func(ctx context.Context, ownerUserID primitive.ObjectID) (bool, *models.Company, error)
	findWEForCompany   func(ctx context.Context, companyID, fallbackUserID primitive.ObjectID) (bool, *models.WebEntity, error)
	findWECByWEAndUser func(ctx context.Context, webEntityID, userID primitive.ObjectID) (bool, *models.WebEntityContext, error)
}

// The three SIE-backstop reads default to "nothing found" instead of the panic
// convention: every activated-subscription apply crosses the backstop, and a
// buyer with no personal company (so no cold start) is the correct default for
// tests that aren't about it.
func (m *mockStore) FindPersonalCompanyByOwner(ctx context.Context, ownerUserID primitive.ObjectID) (bool, *models.Company, error) {
	if m.findPersonalComp == nil {
		return false, nil, nil
	}
	return m.findPersonalComp(ctx, ownerUserID)
}
func (m *mockStore) FindWebEntityForCompany(ctx context.Context, companyID, fallbackUserID primitive.ObjectID) (bool, *models.WebEntity, error) {
	if m.findWEForCompany == nil {
		return false, nil, nil
	}
	return m.findWEForCompany(ctx, companyID, fallbackUserID)
}
func (m *mockStore) FindWebEntityContextByWebEntityAndUser(ctx context.Context, webEntityID, userID primitive.ObjectID) (bool, *models.WebEntityContext, error) {
	if m.findWECByWEAndUser == nil {
		return false, nil, nil
	}
	return m.findWECByWEAndUser(ctx, webEntityID, userID)
}

func (m *mockStore) FindCompanyByID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Company, error) {
	if m.findCompanyByID == nil {
		panic("unexpected FindCompanyByID")
	}
	return m.findCompanyByID(ctx, companyID)
}
func (m *mockStore) FindLatestSubscriptionByCompanyID(ctx context.Context, companyID primitive.ObjectID) (bool, *models.Subscription, error) {
	if m.findLatestByComp == nil {
		panic("unexpected FindLatestSubscriptionByCompanyID")
	}
	return m.findLatestByComp(ctx, companyID)
}
func (m *mockStore) SetCompanyBilling(ctx context.Context, companyID primitive.ObjectID, billing models.CompanyBilling) error {
	if m.setCompanyBilling == nil {
		panic("unexpected SetCompanyBilling")
	}
	return m.setCompanyBilling(ctx, companyID, billing)
}
func (m *mockStore) ListClaimedMembershipUserIDs(ctx context.Context, companyID primitive.ObjectID) ([]primitive.ObjectID, error) {
	if m.listClaimedUserIDs == nil {
		panic("unexpected ListClaimedMembershipUserIDs")
	}
	return m.listClaimedUserIDs(ctx, companyID)
}

// GetLifetimeArticlesGenerated defaults to 0 instead of the panic convention:
// every status read of a live local trial crosses the free-plan article meter,
// and a fresh counter is the correct default for tests that aren't about it.
func (m *mockStore) GetLifetimeArticlesGenerated(ctx context.Context, userID primitive.ObjectID) (int, error) {
	if m.lifetimeArticles == nil {
		return 0, nil
	}
	return m.lifetimeArticles(ctx, userID)
}

// IsSubscriptionTombstoned defaults to "not tombstoned" instead of the panic
// convention: every subscription-event apply crosses the resurrection guard,
// and an intact subscription doc is the correct default for tests that aren't
// about admin deletion.
func (m *mockStore) IsSubscriptionTombstoned(ctx context.Context, paddleSubscriptionID string) (bool, error) {
	if m.isSubTombstoned == nil {
		return false, nil
	}
	return m.isSubTombstoned(ctx, paddleSubscriptionID)
}

func (m *mockStore) FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error) {
	if m.findSubsByUser == nil {
		panic("unexpected FindSubscriptionsByUserID")
	}
	return m.findSubsByUser(ctx, userID)
}

// FindTrialModeWECsForUser defaults to "no trial WECs" instead of the panic
// convention: every activated-subscription apply crosses the expand trigger,
// and the pre-trial world (no trial-mode WECs) is the correct default for
// tests that aren't about the upgrade.
func (m *mockStore) FindTrialModeWECsForUser(ctx context.Context, userID primitive.ObjectID) ([]models.WebEntityContext, error) {
	if m.findTrialWECs == nil {
		return nil, nil
	}
	return m.findTrialWECs(ctx, userID)
}
func (m *mockStore) ClaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) (bool, error) {
	if m.claimWECUpgrade == nil {
		panic("unexpected ClaimWECUpgrade")
	}
	return m.claimWECUpgrade(ctx, wecID)
}
func (m *mockStore) UnclaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) error {
	if m.unclaimWECUpgrade == nil {
		panic("unexpected UnclaimWECUpgrade")
	}
	return m.unclaimWECUpgrade(ctx, wecID)
}

func (m *mockStore) InsertEvent(ctx context.Context, e *models.PaddleEvent) (bool, *models.PaddleEvent, error) {
	if m.insertEvent == nil {
		panic("unexpected InsertEvent")
	}
	return m.insertEvent(ctx, e)
}
func (m *mockStore) MarkEventProcessed(ctx context.Context, eventID, note string) error {
	if m.markProcessed == nil {
		panic("unexpected MarkEventProcessed")
	}
	return m.markProcessed(ctx, eventID, note)
}
func (m *mockStore) MarkEventFailed(ctx context.Context, eventID, errMsg string) error {
	if m.markFailed == nil {
		panic("unexpected MarkEventFailed")
	}
	return m.markFailed(ctx, eventID, errMsg)
}
func (m *mockStore) FindCustomerByPaddleID(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
	if m.findCustomer == nil {
		panic("unexpected FindCustomerByPaddleID")
	}
	return m.findCustomer(ctx, id)
}
func (m *mockStore) UpdateCustomerEmail(ctx context.Context, id, email string) error {
	if m.updateEmail == nil {
		panic("unexpected UpdateCustomerEmail")
	}
	return m.updateEmail(ctx, id, email)
}
func (m *mockStore) InsertCustomer(ctx context.Context, c *models.PaddleCustomer) error {
	if m.insertCustomer == nil {
		panic("unexpected InsertCustomer")
	}
	return m.insertCustomer(ctx, c)
}
func (m *mockStore) FindSubscriptionByPaddleID(ctx context.Context, id string) (bool, *models.Subscription, error) {
	if m.findSubByPaddleID == nil {
		panic("unexpected FindSubscriptionByPaddleID")
	}
	return m.findSubByPaddleID(ctx, id)
}
func (m *mockStore) ApplySubscriptionEvent(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
	if m.applySubscription == nil {
		panic("unexpected ApplySubscriptionEvent")
	}
	return m.applySubscription(ctx, id, at, update)
}
func (m *mockStore) ApplyTransactionEvent(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
	if m.applyTransaction == nil {
		panic("unexpected ApplyTransactionEvent")
	}
	return m.applyTransaction(ctx, id, at, update)
}

func (m *mockStore) FindLatestSubscriptionByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.Subscription, error) {
	if m.findLatestByUser == nil {
		panic("unexpected FindLatestSubscriptionByUserID")
	}
	return m.findLatestByUser(ctx, userID)
}
func (m *mockStore) FindLatestSubscriptionByUserIDForApp(ctx context.Context, userID primitive.ObjectID, appID string) (bool, *models.Subscription, error) {
	if m.findLatestForApp == nil {
		panic("unexpected FindLatestSubscriptionByUserIDForApp")
	}
	return m.findLatestForApp(ctx, userID, appID)
}
func (m *mockStore) SetSubscriptionActive(ctx context.Context, id string) error {
	if m.setActive == nil {
		panic("unexpected SetSubscriptionActive")
	}
	return m.setActive(ctx, id)
}
func (m *mockStore) SetSubscriptionCanceled(ctx context.Context, id string, at time.Time) error {
	if m.setCanceled == nil {
		panic("unexpected SetSubscriptionCanceled")
	}
	return m.setCanceled(ctx, id, at)
}
func (m *mockStore) RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error {
	if m.recompute == nil {
		panic("unexpected RecomputeUserEntitlement")
	}
	return m.recompute(ctx, userID, appID)
}
func (m *mockStore) TrialEligibleIdentity(ctx context.Context, userID primitive.ObjectID, paddleCustomerID, excludePaddleSubID string) (bool, error) {
	if m.trialEligible == nil {
		panic("unexpected TrialEligibleIdentity")
	}
	return m.trialEligible(ctx, userID, paddleCustomerID, excludePaddleSubID)
}
func (m *mockStore) ClaimTrialConversion(ctx context.Context, id string) error {
	if m.claimConversion == nil {
		panic("unexpected ClaimTrialConversion")
	}
	return m.claimConversion(ctx, id)
}
func (m *mockStore) ClaimTrialExpiry(ctx context.Context, id string) error {
	if m.claimExpiry == nil {
		panic("unexpected ClaimTrialExpiry")
	}
	return m.claimExpiry(ctx, id)
}
func (m *mockStore) MarkTrialConversionDispatched(ctx context.Context, id string) error {
	if m.markConvDispatch == nil {
		panic("unexpected MarkTrialConversionDispatched")
	}
	return m.markConvDispatch(ctx, id)
}
func (m *mockStore) MarkTrialExpiryDispatched(ctx context.Context, id string) error {
	if m.markExpDispatch == nil {
		panic("unexpected MarkTrialExpiryDispatched")
	}
	return m.markExpDispatch(ctx, id)
}
func (m *mockStore) InsertTrialSubscription(ctx context.Context, sub *models.Subscription) error {
	if m.insertTrialSub == nil {
		panic("unexpected InsertTrialSubscription")
	}
	return m.insertTrialSub(ctx, sub)
}
func (m *mockStore) SetSubscriptionTrialing(ctx context.Context, id string) error {
	if m.setTrialing == nil {
		panic("unexpected SetSubscriptionTrialing")
	}
	return m.setTrialing(ctx, id)
}
func (m *mockStore) SetTrialValidTill(ctx context.Context, id string, validTill time.Time) error {
	if m.setTrialValidTill == nil {
		panic("unexpected SetTrialValidTill")
	}
	return m.setTrialValidTill(ctx, id, validTill)
}
func (m *mockStore) EndLocalTrialsForUser(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error {
	if m.endLocalTrials == nil {
		panic("unexpected EndLocalTrialsForUser")
	}
	return m.endLocalTrials(ctx, userID, appID, at)
}
func (m *mockStore) IsWebEntityFinalised(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	if m.webEntityFinalised == nil {
		panic("unexpected IsWebEntityFinalised")
	}
	return m.webEntityFinalised(ctx, userID)
}

// mockDispatcher fails loudly unless a test arms it.
type mockDispatcher struct {
	dispatch func(ctx context.Context, processType, userID string, payload any) error
}

func (d *mockDispatcher) Dispatch(ctx context.Context, processType, userID string, payload any) error {
	if d.dispatch == nil {
		panic("unexpected Dispatch: " + processType)
	}
	return d.dispatch(ctx, processType, userID, payload)
}

func (d *mockDispatcher) DispatchKeyed(ctx context.Context, processType, userID, _ string, payload any) error {
	return d.Dispatch(ctx, processType, userID, payload)
}

// armTrialLifecycle satisfies webhook steps (d)/(e) for tests that aren't
// about the trial lifecycle: claims are no-ops and the stored sub carries no
// trial facts, so nothing dispatches.
func armTrialLifecycle(m *mockStore) {
	m.claimConversion = func(ctx context.Context, id string) error { return nil }
	m.claimExpiry = func(ctx context.Context, id string) error { return nil }
	m.findSubByPaddleID = func(ctx context.Context, id string) (bool, *models.Subscription, error) {
		return true, &models.Subscription{PaddleSubscriptionID: id, AppID: models.AppIDIndexly}, nil
	}
	// The local-trial supersede runs on active/trialing applies; a no-op keeps
	// tests that aren't about it from panicking.
	m.endLocalTrials = func(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error { return nil }
}

func (m *mockStore) FindCustomerByUserID(ctx context.Context, userID primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
	if m.findCustomerByUser == nil {
		panic("unexpected FindCustomerByUserID")
	}
	return m.findCustomerByUser(ctx, userID)
}
func (m *mockStore) HasValidSubscription(context.Context, primitive.ObjectID) (bool, error) {
	panic("unexpected HasValidSubscription")
}
func (m *mockStore) SetSubscriptionCanceling(ctx context.Context, id string, scheduledCancelAt *time.Time) error {
	if m.setCanceling == nil {
		panic("unexpected SetSubscriptionCanceling")
	}
	return m.setCanceling(ctx, id, scheduledCancelAt)
}
func (m *mockStore) ListTransactionsByUserID(context.Context, primitive.ObjectID) ([]models.Transaction, error) {
	panic("unexpected ListTransactionsByUserID")
}

// testPlansCache maps pri_1 → indexly so subscription applies resolve. Unmapped
// prices (e.g. pri_unknown) fail closed.
func testPlansCache(t *testing.T) *entitlements.PlansCache {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{{
		AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
		Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_1"}},
		Features: []string{"article.generate"},
	}})
	if err != nil {
		t.Fatalf("test plans cache: %v", err)
	}
	return cache
}

func newTestService(t *testing.T, st store.Store) *service {
	return &service{store: st, paddle: nil, plans: testPlansCache(t), dispatcher: &mockDispatcher{}, productID: "pro_test"}
}

// subItems is the single-item price list carried by every subscription payload
// (the webhook derives app_id from items[0].price.id).
func subItems() []map[string]any {
	return []map[string]any{{"price": map[string]any{"id": "pri_1", "product_id": "pro_test"}}}
}

func freshInsert(m *mockStore) {
	m.insertEvent = func(ctx context.Context, e *models.PaddleEvent) (bool, *models.PaddleEvent, error) {
		return false, nil, nil
	}
}

func subEnvelope(t *testing.T, eventType string, sub map[string]any) dto.WebhookEnvelope {
	t.Helper()
	data, err := json.Marshal(sub)
	if err != nil {
		t.Fatalf("marshal sub payload: %v", err)
	}
	return dto.WebhookEnvelope{
		EventID:    "evt_test",
		EventType:  eventType,
		OccurredAt: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
		Data:       data,
	}
}

func TestHandleWebhookEvent_DuplicateProcessedIsSkipped(t *testing.T) {
	processedAt := time.Now()
	m := &mockStore{
		insertEvent: func(ctx context.Context, e *models.PaddleEvent) (bool, *models.PaddleEvent, error) {
			return true, &models.PaddleEvent{EventID: e.EventID, ProcessedAt: &processedAt}, nil
		},
		// No dispatch, no mark calls allowed — any would panic.
	}
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{"id": "sub_1"})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("duplicate processed event must ack (nil), got: %v", err)
	}
}

func TestHandleWebhookEvent_PreviouslyFailedEventIsReprocessed(t *testing.T) {
	userID := primitive.NewObjectID()
	applied := false
	marked := false
	m := &mockStore{
		insertEvent: func(ctx context.Context, e *models.PaddleEvent) (bool, *models.PaddleEvent, error) {
			// Row exists from a failed attempt: no processed_at.
			return true, &models.PaddleEvent{EventID: e.EventID, ProcessingError: "boom"}, nil
		},
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID, PaddleCustomerID: id}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			applied = true
			return true, nil
		},
		recompute: func(ctx context.Context, userID primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error {
			marked = true
			if note != "" {
				t.Errorf("expected empty note for applied event, got %q", note)
			}
			return nil
		},
	}
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
		"current_billing_period": map[string]any{
			"starts_at": "2026-06-01T00:00:00Z", "ends_at": "2026-07-01T00:00:00Z",
		},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("reprocess must succeed, got: %v", err)
	}
	if !applied {
		t.Error("previously failed event was not re-applied")
	}
	if !marked {
		t.Error("reprocessed event was not marked processed")
	}
}

func TestHandleWebhookEvent_TransientErrorMarksFailedAndReturnsError(t *testing.T) {
	markedFailed := false
	m := &mockStore{
		insertEvent: nil, // set below
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return false, nil, errors.New("mongo down")
		},
		markFailed: func(ctx context.Context, eventID, errMsg string) error {
			markedFailed = true
			return nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1",
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("transient store error must propagate (500 → Paddle retries)")
	}
	if !markedFailed {
		t.Error("transient failure was not recorded on the event row")
	}
}

func TestHandleWebhookEvent_UnresolvableUserIsAckedWithNote(t *testing.T) {
	var note string
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return false, nil, nil // no mapping (simulator payload)
		},
		markProcessed: func(ctx context.Context, eventID, n string) error {
			note = n
			return nil
		},
		// applySubscription absent: applying would panic.
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	// No custom_data.userId either — must be acked, never retried.
	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_sim", "status": "active", "customer_id": "ctm_unknown",
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("unresolvable event must ack (nil), got: %v", err)
	}
	if note == "" {
		t.Error("unresolvable event must record a note on the event row")
	}
}

func TestHandleWebhookEvent_UnknownEventTypeIsAcked(t *testing.T) {
	var note string
	m := &mockStore{
		markProcessed: func(ctx context.Context, eventID, n string) error {
			note = n
			return nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := dto.WebhookEnvelope{
		EventID:    "evt_x",
		EventType:  "address.created",
		OccurredAt: time.Now(),
		Data:       json.RawMessage(`{}`),
	}
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("unknown event type must ack, got: %v", err)
	}
	if note == "" {
		t.Error("ignored event should carry a note")
	}
}

func TestHandleWebhookEvent_MissingEnvelopeFieldsAcked(t *testing.T) {
	s := newTestService(t, &mockStore{}) // any store call would panic
	if err := s.HandleWebhookEvent(context.Background(), dto.WebhookEnvelope{}, nil); err != nil {
		t.Fatalf("empty envelope must ack without processing, got: %v", err)
	}
}

func TestHandleWebhookEvent_StaleEventSkippedByOrderingGuard(t *testing.T) {
	userID := primitive.NewObjectID()
	marked := false
	recomputed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return false, nil // ordering guard: event older than doc state
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed = true
			if uid != userID || appID != models.AppIDIndexly {
				t.Errorf("recompute(%s, %s), want (%s, indexly)", uid.Hex(), appID, userID.Hex())
			}
			return nil
		},
		markProcessed: func(ctx context.Context, eventID, note string) error {
			marked = true
			return nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("stale event must still ack, got: %v", err)
	}
	if !marked {
		t.Error("stale event must still be marked processed")
	}
	// The heal path: a redelivered event whose apply was absorbed must STILL
	// recompute — a crash between apply and recompute would otherwise leave
	// the projection stale forever.
	if !recomputed {
		t.Error("recompute must run even when applied==false")
	}
}

func TestApplySubscription_CanceledClampsValidTill(t *testing.T) {
	userID := primitive.NewObjectID()
	canceledAt := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)
	var captured bson.M
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute:     func(ctx context.Context, userID primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	// Immediate cancellation: valid_till (period end) is still in the future.
	env := subEnvelope(t, dto.EventSubscriptionCanceled, map[string]any{
		"id": "sub_1", "status": "canceled", "customer_id": "ctm_1", "items": subItems(),
		"canceled_at": canceledAt.Format(time.RFC3339),
		"current_billing_period": map[string]any{
			"starts_at": "2026-06-01T00:00:00Z", "ends_at": "2026-07-01T00:00:00Z",
		},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("canceled event failed: %v", err)
	}

	min, ok := captured["$min"].(bson.M)
	if !ok {
		t.Fatalf("canceled event must clamp valid_till via $min, update: %v", captured)
	}
	if got := min["valid_till"].(time.Time); !got.Equal(canceledAt) {
		t.Errorf("clamp must use payload canceled_at; got %v want %v", got, canceledAt)
	}
	set := captured["$set"].(bson.M)
	if _, exists := set["valid_till"]; exists {
		t.Error("$set must not also carry valid_till when $min clamps it")
	}
	if set["status"] != models.SubStatusCanceled {
		t.Errorf("status = %v, want canceled", set["status"])
	}
}

func TestApplySubscription_ScheduledCancelSetAndCleared(t *testing.T) {
	userID := primitive.NewObjectID()
	var captured bson.M
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute:     func(ctx context.Context, userID primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	// Cancel scheduled → status canceling, scheduled_cancel_at set.
	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
		"scheduled_change": map[string]any{
			"action": "cancel", "effective_at": "2026-07-01T00:00:00Z",
		},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("canceling event failed: %v", err)
	}
	set := captured["$set"].(bson.M)
	if set["status"] != models.SubStatusCanceling {
		t.Errorf("status = %v, want canceling", set["status"])
	}
	if _, ok := set["scheduled_cancel_at"]; !ok {
		t.Error("scheduled_cancel_at must be set when scheduled_change.action == cancel")
	}

	// Un-cancel confirmation → scheduled_cancel_at must be $unset, not left stale.
	env2 := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	env2.EventID = "evt_test_2"
	if err := s.HandleWebhookEvent(context.Background(), env2, []byte("{}")); err != nil {
		t.Fatalf("un-cancel event failed: %v", err)
	}
	set = captured["$set"].(bson.M)
	if set["status"] != models.SubStatusActive {
		t.Errorf("status = %v, want active", set["status"])
	}
	unset, ok := captured["$unset"].(bson.M)
	if !ok {
		t.Fatal("un-cancel must carry $unset")
	}
	if _, ok := unset["scheduled_cancel_at"]; !ok {
		t.Error("cleared scheduled change must $unset scheduled_cancel_at")
	}
}

func TestApplySubscription_PausedKeepsStoredValidTill(t *testing.T) {
	userID := primitive.NewObjectID()
	var captured bson.M
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute:     func(ctx context.Context, userID primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	// Paused payloads have current_billing_period == null — valid_till must
	// not be written (paid-through rule keeps the stored value).
	env := subEnvelope(t, dto.EventSubscriptionPaused, map[string]any{
		"id": "sub_1", "status": "paused", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("paused event failed: %v", err)
	}
	set := captured["$set"].(bson.M)
	if set["status"] != models.SubStatusPaused {
		t.Errorf("status = %v, want paused", set["status"])
	}
	if _, exists := set["valid_till"]; exists {
		t.Error("paused payload must not overwrite valid_till (billing period is null)")
	}
}

func TestApplyTransaction_FailedThenCompletedSameIDUpserts(t *testing.T) {
	userID := primitive.NewObjectID()
	subID := primitive.NewObjectID()
	var statuses []string
	m := &mockStore{
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, &models.Subscription{ID: subID, UserID: userID, PaddleSubscriptionID: id}, nil
		},
		applyTransaction: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			if id != "txn_1" {
				t.Errorf("transaction id = %s, want txn_1", id)
			}
			statuses = append(statuses, update["$set"].(bson.M)["status"].(string))
			return true, nil
		},
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	txn := map[string]any{
		"id": "txn_1", "subscription_id": "sub_1", "customer_id": "ctm_1",
		"currency_code": "USD",
		"details":       map[string]any{"totals": map[string]any{"total": "2900"}},
	}

	// Paddle retries payment on the SAME transaction ID: failed first…
	envFail := subEnvelope(t, dto.EventTransactionPaymentFailed, txn)
	envFail.EventID = "evt_fail"
	if err := s.HandleWebhookEvent(context.Background(), envFail, []byte("{}")); err != nil {
		t.Fatalf("failed-txn event errored: %v", err)
	}
	// …then completed must overwrite, not be skipped by a dup-key insert.
	envOK := subEnvelope(t, dto.EventTransactionCompleted, txn)
	envOK.EventID = "evt_ok"
	envOK.OccurredAt = envOK.OccurredAt.Add(time.Hour)
	if err := s.HandleWebhookEvent(context.Background(), envOK, []byte("{}")); err != nil {
		t.Fatalf("completed-txn event errored: %v", err)
	}

	want := fmt.Sprintf("[%s %s]", models.TxnStatusFailed, models.TxnStatusCompleted)
	if got := fmt.Sprintf("%v", statuses); got != want {
		t.Errorf("transaction statuses applied = %v, want %v", got, want)
	}
}

func TestApplySubscription_UnmappedPriceQuarantined(t *testing.T) {
	userID := primitive.NewObjectID()
	markedFailed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		markFailed: func(ctx context.Context, eventID, errMsg string) error {
			markedFailed = true
			return nil
		},
		// applySubscription / recompute absent: applying an unmapped price
		// would panic — nothing may be written.
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1",
		"items": []map[string]any{{"price": map[string]any{"id": "pri_unknown", "product_id": "pro_x"}}},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("unmapped price must fail the event (Paddle redelivers after the plan doc exists)")
	}
	if !markedFailed {
		t.Error("quarantined event must be recorded as failed, not processed")
	}
}

func TestApplySubscription_TrialingLatchesAndFlagsRetrial(t *testing.T) {
	userID := primitive.NewObjectID()
	var captured bson.M
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, customerID, excludeSubID string) (bool, error) {
			if excludeSubID != "sub_trial" {
				t.Errorf("eligibility must exclude the sub being processed, got %q", excludeSubID)
			}
			return false, nil // identity has prior history
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionTrialing, map[string]any{
		"id": "sub_trial", "status": "trialing", "customer_id": "ctm_1", "items": subItems(),
		"current_billing_period": map[string]any{
			"starts_at": "2026-06-12T00:00:00Z", "ends_at": "2026-06-19T00:00:00Z",
		},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("trialing event failed: %v", err)
	}

	set := captured["$set"].(bson.M)
	if set["status"] != models.SubStatusTrialing {
		t.Errorf("status = %v, want trialing", set["status"])
	}
	if set["app_id"] != models.AppIDIndexly {
		t.Errorf("app_id = %v, want indexly (derived from price, never fallback)", set["app_id"])
	}
	if set["was_trialing"] != true {
		t.Error("trialing event must latch was_trialing")
	}
	if set["trial_flagged"] != true {
		t.Error("re-trial identity must be flagged")
	}
}

func TestApplySubscription_EligibleTrialNotFlagged(t *testing.T) {
	userID := primitive.NewObjectID()
	var captured bson.M
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, customerID, excludeSubID string) (bool, error) {
			return true, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionTrialing, map[string]any{
		"id": "sub_trial", "status": "trialing", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("trialing event failed: %v", err)
	}
	set := captured["$set"].(bson.M)
	if set["was_trialing"] != true {
		t.Error("was_trialing must latch for every trialing apply")
	}
	if _, flagged := set["trial_flagged"]; flagged {
		t.Error("eligible trial must not be flagged")
	}
}

func TestApplySubscription_RecomputeFailureFailsEvent(t *testing.T) {
	userID := primitive.NewObjectID()
	markedFailed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			return errors.New("mongo blip")
		},
		markFailed: func(ctx context.Context, eventID, errMsg string) error {
			markedFailed = true
			return nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("recompute failure must fail the event so Paddle redelivers")
	}
	if !markedFailed {
		t.Error("recompute failure must be recorded on the event row")
	}
}

// trialLifecycleHarness returns a store armed for a subscription apply where
// the stored sub carries the given trial facts, capturing claim calls.
func trialLifecycleHarness(t *testing.T, userID primitive.ObjectID, stored *models.Subscription) (*mockStore, *[]string) {
	t.Helper()
	var claims []string
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		claimConversion: func(ctx context.Context, id string) error {
			claims = append(claims, "conversion")
			return nil
		},
		claimExpiry: func(ctx context.Context, id string) error {
			claims = append(claims, "expiry")
			return nil
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, stored, nil
		},
		markProcessed:  func(ctx context.Context, eventID, note string) error { return nil },
		endLocalTrials: func(ctx context.Context, userID primitive.ObjectID, appID string, at time.Time) error { return nil },
	}
	freshInsert(m)
	return m, &claims
}

func TestTrialClaims_KeyedOnRawStatus(t *testing.T) {
	userID := primitive.NewObjectID()
	bare := &models.Subscription{PaddleSubscriptionID: "sub_1", AppID: models.AppIDIndexly, WasTrialing: true}

	cases := []struct {
		name       string
		payload    map[string]any
		wantClaims string
	}{
		{
			// A mid-trial cancel is only a scheduled_change: raw status stays
			// "trialing" and must NEVER claim a conversion.
			name: "mid-trial cancel never claims",
			payload: map[string]any{
				"id": "sub_1", "status": "trialing", "customer_id": "ctm_1", "items": subItems(),
				"scheduled_change": map[string]any{"action": "cancel", "effective_at": "2026-07-01T00:00:00Z"},
			},
			wantClaims: "[]",
		},
		{
			// Convert-then-schedule-cancel arrives raw "active" + scheduled
			// change: internal status maps to canceling, but the RAW status
			// still claims the conversion.
			name: "convert then schedule cancel still claims",
			payload: map[string]any{
				"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
				"scheduled_change": map[string]any{"action": "cancel", "effective_at": "2026-07-01T00:00:00Z"},
			},
			wantClaims: "[conversion]",
		},
		{
			name: "canceled claims expiry",
			payload: map[string]any{
				"id": "sub_1", "status": "canceled", "customer_id": "ctm_1", "items": subItems(),
			},
			wantClaims: "[expiry]",
		},
		{
			name: "paused claims expiry",
			payload: map[string]any{
				"id": "sub_1", "status": "paused", "customer_id": "ctm_1", "items": subItems(),
			},
			wantClaims: "[expiry]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, claims := trialLifecycleHarness(t, userID, bare)
			if tc.payload["status"] == "trialing" {
				m.trialEligible = func(ctx context.Context, uid primitive.ObjectID, c, e string) (bool, error) {
					return true, nil
				}
			}
			s := newTestService(t, m)
			env := subEnvelope(t, dto.EventSubscriptionUpdated, tc.payload)
			if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
				t.Fatalf("event failed: %v", err)
			}
			if got := fmt.Sprintf("%v", *claims); got != tc.wantClaims {
				t.Errorf("claims = %v, want %v", got, tc.wantClaims)
			}
		})
	}
}

func TestTrialDispatch_FactSetMarkerUnsetDispatchesAndMarks(t *testing.T) {
	userID := primitive.NewObjectID()
	convertedAt := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	stored := &models.Subscription{
		PaddleSubscriptionID: "sub_1", AppID: models.AppIDIndexly, PriceID: "pri_1",
		WasTrialing: true, TrialConvertedAt: &convertedAt,
		// TrialConvertedDispatchedAt nil → must dispatch.
	}
	m, _ := trialLifecycleHarness(t, userID, stored)
	marked := false
	m.markConvDispatch = func(ctx context.Context, id string) error {
		marked = true
		return nil
	}
	s := newTestService(t, m)

	var dispatched []string
	s.dispatcher = &mockDispatcher{dispatch: func(ctx context.Context, processType, uid string, payload any) error {
		dispatched = append(dispatched, processType)
		p, ok := payload.(entitlements.TrialConvertedPayload)
		if !ok {
			t.Fatalf("payload type %T", payload)
		}
		if p.PaddleSubscriptionID != "sub_1" || p.AppID != models.AppIDIndexly || p.PriceID != "pri_1" || !p.ConvertedAt.Equal(convertedAt) {
			t.Errorf("payload = %+v", p)
		}
		if uid != userID.Hex() {
			t.Errorf("dispatch user = %s, want %s", uid, userID.Hex())
		}
		return nil
	}}

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
	if fmt.Sprintf("%v", dispatched) != "["+string(entitlements.ProcessTrialConverted)+"]" {
		t.Errorf("dispatched = %v, want exactly one trial_converted", dispatched)
	}
	if !marked {
		t.Error("successful dispatch must stamp the dispatch marker")
	}
}

func TestTrialDispatch_AlreadyMarkedDoesNotRedispatch(t *testing.T) {
	userID := primitive.NewObjectID()
	convertedAt := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	stored := &models.Subscription{
		PaddleSubscriptionID: "sub_1", AppID: models.AppIDIndexly, PriceID: "pri_1",
		WasTrialing: true, TrialConvertedAt: &convertedAt, TrialConvertedDispatchedAt: &convertedAt,
	}
	m, _ := trialLifecycleHarness(t, userID, stored)
	s := newTestService(t, m)
	// Default mockDispatcher panics on any Dispatch — both markers set means
	// no dispatch may happen.

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

func TestTrialDispatch_DispatchFailureFailsEventMarkerStaysUnset(t *testing.T) {
	userID := primitive.NewObjectID()
	convertedAt := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	stored := &models.Subscription{
		PaddleSubscriptionID: "sub_1", AppID: models.AppIDIndexly, PriceID: "pri_1",
		WasTrialing: true, TrialConvertedAt: &convertedAt,
	}
	m, _ := trialLifecycleHarness(t, userID, stored)
	markedFailed := false
	m.markFailed = func(ctx context.Context, eventID, errMsg string) error {
		markedFailed = true
		return nil
	}
	// markConvDispatch left nil: stamping after a failed dispatch would panic.
	s := newTestService(t, m)
	s.dispatcher = &mockDispatcher{dispatch: func(ctx context.Context, pt, uid string, payload any) error {
		return errors.New("sqs down")
	}}

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("dispatch failure must fail the event — redelivery retries the dispatch (fact stays claimed, marker unset)")
	}
	if !markedFailed {
		t.Error("dispatch failure must be recorded on the event row")
	}
}

func TestTrialDispatch_MarkDispatchedFailureTolerated(t *testing.T) {
	userID := primitive.NewObjectID()
	expiredAt := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	stored := &models.Subscription{
		PaddleSubscriptionID: "sub_1", AppID: models.AppIDIndexly, PriceID: "pri_1",
		WasTrialing: true, TrialExpiredAt: &expiredAt,
	}
	m, _ := trialLifecycleHarness(t, userID, stored)
	m.markExpDispatch = func(ctx context.Context, id string) error {
		return errors.New("mongo blip")
	}
	s := newTestService(t, m)
	s.dispatcher = &mockDispatcher{dispatch: func(ctx context.Context, pt, uid string, payload any) error {
		return nil
	}}

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "canceled", "customer_id": "ctm_1", "items": subItems(),
	})
	// A failed marker stamp means one duplicate dispatch on the next
	// redelivery — tolerated, never a failed event.
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("MarkDispatched failure must not fail the event, got: %v", err)
	}
}

// mockPaddle implements interfaces.PaddleClient; only the overridden calls are
// expected.
type mockPaddle struct {
	cancelImmediately func(ctx context.Context, subscriptionID string) (*appdto.PaddleSubscription, error)
	setNextBilledAt   func(ctx context.Context, subscriptionID string, at time.Time) (*appdto.PaddleSubscription, error)
	activateTrialing  func(ctx context.Context, subscriptionID string) (*appdto.PaddleSubscription, error)
	listActivePrices  func(ctx context.Context, productID string) ([]appdto.PaddlePrice, error)
	cancelAtPeriodEnd func(ctx context.Context, subscriptionID string) (*appdto.PaddleSubscription, error)
	removeScheduled   func(ctx context.Context, subscriptionID string) (*appdto.PaddleSubscription, error)
	listLiveSubs      func(ctx context.Context, customerID string) ([]appdto.PaddleSubscription, error)
}

func (p *mockPaddle) CancelImmediately(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
	if p.cancelImmediately == nil {
		panic("unexpected CancelImmediately")
	}
	return p.cancelImmediately(ctx, id)
}
func (p *mockPaddle) SetNextBilledAt(ctx context.Context, id string, at time.Time) (*appdto.PaddleSubscription, error) {
	if p.setNextBilledAt == nil {
		panic("unexpected SetNextBilledAt")
	}
	return p.setNextBilledAt(ctx, id, at)
}
func (p *mockPaddle) ActivateTrialingSubscription(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
	if p.activateTrialing == nil {
		panic("unexpected ActivateTrialingSubscription")
	}
	return p.activateTrialing(ctx, id)
}
func (p *mockPaddle) CreateCustomer(context.Context, string, map[string]any) (*appdto.PaddleCustomer, error) {
	panic("unexpected CreateCustomer")
}
func (p *mockPaddle) GetCustomerByEmail(context.Context, string) (*appdto.PaddleCustomer, error) {
	panic("unexpected GetCustomerByEmail")
}
func (p *mockPaddle) ReactivateCustomer(context.Context, string) error {
	panic("unexpected ReactivateCustomer")
}
func (p *mockPaddle) ListActivePrices(ctx context.Context, productID string) ([]appdto.PaddlePrice, error) {
	if p.listActivePrices == nil {
		panic("unexpected ListActivePrices")
	}
	return p.listActivePrices(ctx, productID)
}
func (p *mockPaddle) GetSubscription(context.Context, string) (*appdto.PaddleSubscription, error) {
	panic("unexpected GetSubscription")
}
func (p *mockPaddle) ListLiveSubscriptionsByCustomer(ctx context.Context, customerID string) ([]appdto.PaddleSubscription, error) {
	if p.listLiveSubs == nil {
		panic("unexpected ListLiveSubscriptionsByCustomer")
	}
	return p.listLiveSubs(ctx, customerID)
}
func (p *mockPaddle) CancelAtPeriodEnd(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
	if p.cancelAtPeriodEnd == nil {
		panic("unexpected CancelAtPeriodEnd")
	}
	return p.cancelAtPeriodEnd(ctx, id)
}
func (p *mockPaddle) RemoveScheduledCancellation(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
	if p.removeScheduled == nil {
		panic("unexpected RemoveScheduledCancellation")
	}
	return p.removeScheduled(ctx, id)
}
func (p *mockPaddle) VerifyWebhook(*http.Request, []byte) error {
	panic("unexpected VerifyWebhook")
}

func TestCancelImmediately_RecomputesProjectionAfterClamp(t *testing.T) {
	userID := primitive.NewObjectID()
	var calls []string
	m := &mockStore{
		findLatestByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				UserID:               uid,
				PaddleSubscriptionID: "sub_1",
				Status:               models.SubStatusActive,
				ValidTill:            time.Now().Add(24 * time.Hour),
				// AppID empty: legacy doc from before the backfill.
			}, nil
		},
		setCanceled: func(ctx context.Context, id string, at time.Time) error {
			calls = append(calls, "clamp")
			return nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			calls = append(calls, "recompute")
			if appID != models.AppIDIndexly {
				t.Errorf("legacy doc must recompute as indexly, got %q", appID)
			}
			return nil
		},
	}
	s := newTestService(t, m)
	s.paddle = &mockPaddle{
		cancelImmediately: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
			return &appdto.PaddleSubscription{ID: id, Status: "canceled"}, nil
		},
	}

	if err := s.CancelSubscriptionImmediately(context.Background(), userID); err != nil {
		t.Fatalf("cancel immediately failed: %v", err)
	}
	// The projection must be clamped in the same request — before any webhook
	// lands — to preserve today's immediate-revocation property.
	if got := fmt.Sprintf("%v", calls); got != "[clamp recompute]" {
		t.Errorf("call order = %v, want [clamp recompute]", calls)
	}
}

func TestMapPaddleSubscriptionStatus(t *testing.T) {
	cancelChange := &dto.WebhookScheduledChange{Action: "cancel", EffectiveAt: time.Now()}
	cases := []struct {
		name string
		data dto.WebhookSubscription
		want string
	}{
		{"active", dto.WebhookSubscription{Status: "active"}, models.SubStatusActive},
		{"trialing preserved", dto.WebhookSubscription{Status: "trialing"}, models.SubStatusTrialing},
		{"trialing with scheduled cancel stays trialing", dto.WebhookSubscription{Status: "trialing", ScheduledChange: cancelChange}, models.SubStatusTrialing},
		{"active with scheduled cancel", dto.WebhookSubscription{Status: "active", ScheduledChange: cancelChange}, models.SubStatusCanceling},
		{"past_due", dto.WebhookSubscription{Status: "past_due"}, models.SubStatusPastDue},
		{"canceled", dto.WebhookSubscription{Status: "canceled"}, models.SubStatusCanceled},
		{"paused", dto.WebhookSubscription{Status: "paused"}, models.SubStatusPaused},
		{"unknown stored raw", dto.WebhookSubscription{Status: "weird_new_status"}, "weird_new_status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapPaddleSubscriptionStatus(tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An activated Indexly subscription for a user with a trial-mode WEC must
// claim the expand (CAS) and dispatch SIE_UPGRADE_EXPAND with the WEC's IDs.
func TestWebhookUpgradeExpand_ClaimsAndDispatches(t *testing.T) {
	userID := primitive.NewObjectID()
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	claimed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
		findTrialWECs: func(ctx context.Context, uid primitive.ObjectID) ([]models.WebEntityContext, error) {
			if uid != userID {
				t.Errorf("trial WEC scan for %v, want %v", uid, userID)
			}
			return []models.WebEntityContext{{ID: wecID, WebEntityID: weID, SIEMode: models.SIEModeTrial}}, nil
		},
		claimWECUpgrade: func(ctx context.Context, id primitive.ObjectID) (bool, error) {
			if id != wecID {
				t.Errorf("claim for WEC %v, want %v", id, wecID)
			}
			claimed = true
			return true, nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)

	var dispatchedType string
	var dispatchedPayload any
	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			dispatchedType = processType
			dispatchedPayload = payload
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
	if !claimed {
		t.Fatal("expected upgrade CAS claim")
	}
	if dispatchedType != "SIE_UPGRADE_EXPAND" {
		t.Fatalf("dispatched %q, want SIE_UPGRADE_EXPAND", dispatchedType)
	}
	p, ok := dispatchedPayload.(pipeline.StandardPayload)
	if !ok || p.WebEntityContextID != wecID.Hex() || p.WebEntityID != weID.Hex() {
		t.Errorf("payload = %#v, want wec %s / we %s", dispatchedPayload, wecID.Hex(), weID.Hex())
	}
}

// A Paddle redelivery loses the CAS and must NOT dispatch a second expand.
func TestWebhookUpgradeExpand_RedeliveryNoOpsOnCAS(t *testing.T) {
	userID := primitive.NewObjectID()
	wecID := primitive.NewObjectID()

	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
		findTrialWECs: func(ctx context.Context, uid primitive.ObjectID) ([]models.WebEntityContext, error) {
			return []models.WebEntityContext{{ID: wecID, SIEMode: models.SIEModeTrial, UpgradeState: models.WECUpgradeStateExpanding}}, nil
		},
		claimWECUpgrade: func(ctx context.Context, id primitive.ObjectID) (bool, error) {
			return false, nil // already claimed by the first delivery
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			if processType == "SIE_UPGRADE_EXPAND" {
				t.Error("redelivery must not re-dispatch the expand")
			}
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// A raw-trialing Paddle sub (paid checkout with Paddle-side trial) supersedes
// the local trial but must NOT trigger the expand — only activation does.
func TestWebhookUpgradeExpand_TrialingDoesNotTrigger(t *testing.T) {
	userID := primitive.NewObjectID()
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
		trialEligible: func(ctx context.Context, uid primitive.ObjectID, customerID, exclude string) (bool, error) {
			return true, nil
		},
		findTrialWECs: func(ctx context.Context, uid primitive.ObjectID) ([]models.WebEntityContext, error) {
			t.Error("trialing status must not scan for expand candidates")
			return nil, nil
		},
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionTrialing, map[string]any{
		"id": "sub_t", "status": "trialing", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// --- SIE cold-start backstop -------------------------------------------------

// backstopStore builds the mock every backstop test starts from: a resolvable
// buyer whose apply succeeds, with the trial lifecycle armed.
func backstopStore(t *testing.T, userID primitive.ObjectID) *mockStore {
	t.Helper()
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	return m
}

// armPersonalCompany wires the personal company + its web entity. wecFound
// controls the "SIE already ran" precondition.
func armPersonalCompany(t *testing.T, m *mockStore, userID, companyID, weID primitive.ObjectID, finalised, wecFound bool) {
	t.Helper()
	m.findPersonalComp = func(ctx context.Context, owner primitive.ObjectID) (bool, *models.Company, error) {
		if owner != userID {
			t.Errorf("personal company lookup for %v, want owner %v", owner, userID)
		}
		return true, &models.Company{ID: companyID, Kind: models.CompanyKindPersonal, OwnerUserID: userID}, nil
	}
	m.findWEForCompany = func(ctx context.Context, cid, fallbackUserID primitive.ObjectID) (bool, *models.WebEntity, error) {
		if cid != companyID {
			t.Errorf("web entity lookup for company %v, want %v", cid, companyID)
		}
		if fallbackUserID != userID {
			t.Errorf("legacy fallback user %v, want %v", fallbackUserID, userID)
		}
		return true, &models.WebEntity{ID: weID, CompanyID: companyID, UserID: userID, Finalised: finalised}, nil
	}
	m.findWECByWEAndUser = func(ctx context.Context, we, uid primitive.ObjectID) (bool, *models.WebEntityContext, error) {
		if we != weID || uid != userID {
			t.Errorf("WEC lookup for (%v, %v), want (%v, %v)", we, uid, weID, userID)
		}
		if !wecFound {
			return false, nil, nil
		}
		return true, &models.WebEntityContext{ID: primitive.NewObjectID(), WebEntityID: weID, UserID: userID}, nil
	}
}

// The cold start: paid activation, finalised personal-company entity, no WEC.
func TestSIEBackstop_ColdStartDispatches(t *testing.T) {
	userID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	m := backstopStore(t, userID)
	armPersonalCompany(t, m, userID, companyID, weID, true, false)

	var gotType, gotUser string
	var gotPayload any
	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			gotType, gotUser, gotPayload = processType, uid, payload
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
	if gotType != "SITE_INTELLIGENCE_PROCESS" {
		t.Fatalf("dispatched %q, want SITE_INTELLIGENCE_PROCESS", gotType)
	}
	if gotUser != userID.Hex() {
		t.Errorf("dispatched for user %s, want %s", gotUser, userID.Hex())
	}
	p, ok := gotPayload.(sieBackstopPayload)
	if !ok || p.WebEntityID != weID.Hex() {
		t.Errorf("payload = %#v, want web entity %s", gotPayload, weID.Hex())
	}
}

// An existing WEC — live, done, or errored — means SIE already owns the entity.
// This precondition is also what makes Paddle redeliveries no-op.
func TestSIEBackstop_ExistingWECNoOps(t *testing.T) {
	userID := primitive.NewObjectID()
	m := backstopStore(t, userID)
	armPersonalCompany(t, m, userID, primitive.NewObjectID(), primitive.NewObjectID(), true, true)

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			t.Errorf("must not dispatch %s when a WEC exists", processType)
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// Paid before finishing onboarding: the strategy poll triggers on finalise.
func TestSIEBackstop_NotFinalisedNoOps(t *testing.T) {
	userID := primitive.NewObjectID()
	m := backstopStore(t, userID)
	armPersonalCompany(t, m, userID, primitive.NewObjectID(), primitive.NewObjectID(), false, false)
	m.findWECByWEAndUser = func(ctx context.Context, we, uid primitive.ObjectID) (bool, *models.WebEntityContext, error) {
		t.Error("unfinalised entity must short-circuit before the WEC read")
		return false, nil, nil
	}

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			t.Errorf("must not dispatch %s for an unfinalised entity", processType)
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// No personal company (data gap) is logged and skipped, never a failed event.
func TestSIEBackstop_NoPersonalCompanyNoOps(t *testing.T) {
	userID := primitive.NewObjectID()
	m := backstopStore(t, userID)
	m.findPersonalComp = func(ctx context.Context, owner primitive.ObjectID) (bool, *models.Company, error) {
		return false, nil, nil
	}
	m.findWEForCompany = func(ctx context.Context, cid, fallback primitive.ObjectID) (bool, *models.WebEntity, error) {
		t.Error("no personal company must short-circuit before the web entity read")
		return false, nil, nil
	}

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			t.Errorf("must not dispatch %s without a personal company", processType)
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// A team seat sub pays for a team company — it must never cold-start the
// buyer's PERSONAL company web entity.
func TestSIEBackstop_CompanySubSkipped(t *testing.T) {
	userID := primitive.NewObjectID()
	teamCompanyID := primitive.NewObjectID()

	m := backstopStore(t, userID)
	m.findCompanyByID = func(ctx context.Context, cid primitive.ObjectID) (bool, *models.Company, error) {
		return true, &models.Company{ID: teamCompanyID, Kind: "team", OwnerUserID: userID}, nil
	}
	m.setCompanyBilling = func(ctx context.Context, cid primitive.ObjectID, billing models.CompanyBilling) error {
		return nil
	}
	m.listClaimedUserIDs = func(ctx context.Context, cid primitive.ObjectID) ([]primitive.ObjectID, error) {
		return nil, nil
	}
	m.findPersonalComp = func(ctx context.Context, owner primitive.ObjectID) (bool, *models.Company, error) {
		t.Error("a company-linked sub must not resolve the buyer's personal company")
		return false, nil, nil
	}

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			if processType == "SITE_INTELLIGENCE_PROCESS" {
				t.Error("team sub must not trigger the personal-company backstop")
			}
			return nil
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_team", "status": "active", "customer_id": "ctm_1", "items": subItems(),
		"custom_data": map[string]any{"companyId": teamCompanyID.Hex()},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// A raw-trialing sub does not cold-start: only activation does.
func TestSIEBackstop_TrialingDoesNotTrigger(t *testing.T) {
	userID := primitive.NewObjectID()
	m := backstopStore(t, userID)
	m.trialEligible = func(ctx context.Context, uid primitive.ObjectID, customerID, exclude string) (bool, error) {
		return true, nil
	}
	m.findPersonalComp = func(ctx context.Context, owner primitive.ObjectID) (bool, *models.Company, error) {
		t.Error("trialing status must not run the backstop")
		return false, nil, nil
	}
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionTrialing, map[string]any{
		"id": "sub_t", "status": "trialing", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
}

// The backstop is best effort: a dispatch failure logs and lets the event pass.
// Failing an entitlement-critical webhook for a secondary trigger would be the
// worse trade — the strategy poll still starts the run.
func TestSIEBackstop_DispatchFailureDoesNotFailEvent(t *testing.T) {
	userID := primitive.NewObjectID()
	m := backstopStore(t, userID)
	armPersonalCompany(t, m, userID, primitive.NewObjectID(), primitive.NewObjectID(), true, false)

	processed := false
	m.markProcessed = func(ctx context.Context, eventID, note string) error {
		processed = true
		return nil
	}
	m.markFailed = func(ctx context.Context, eventID, errMsg string) error {
		t.Errorf("backstop dispatch failure must not fail the event: %s", errMsg)
		return nil
	}

	s := &service{store: m, plans: testPlansCache(t), productID: "pro_test", dispatcher: &mockDispatcher{
		dispatch: func(ctx context.Context, processType, uid string, payload any) error {
			return errors.New("sqs down")
		},
	}}

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_paid", "status": "active", "customer_id": "ctm_1", "items": subItems(),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("event failed: %v", err)
	}
	if !processed {
		t.Error("event should have been marked processed")
	}
}
