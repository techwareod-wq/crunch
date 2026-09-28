package service

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

// Company-linkage webhook tests (team seat billing). The mockStore panic
// convention doubles as the "individual machinery untouched" assertion: the
// company path arms NO trial claims, NO local-trial supersede, NO upgrade
// expand — any such call panics the test.

// companySubItems mirrors subItems with a Paddle line-item quantity.
func companySubItems(qty int) []map[string]any {
	return []map[string]any{{
		"price":    map[string]any{"id": "pri_1", "product_id": "pro_team_test"},
		"quantity": qty,
	}}
}

func TestApplySubscription_CompanyLinkageStampsAndSyncsSeats(t *testing.T) {
	ownerID := primitive.NewObjectID()
	memberID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	validTill := time.Now().UTC().Add(30 * 24 * time.Hour)

	var captured bson.M
	var billed *models.CompanyBilling
	recomputed := map[primitive.ObjectID]int{}
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: ownerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			if id != companyID {
				t.Errorf("FindCompanyByID(%s), want %s", id.Hex(), companyID.Hex())
			}
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: ownerID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				PaddleSubscriptionID: id,
				AppID:                models.AppIDIndexly,
				Status:               models.SubStatusActive,
				ValidTill:            validTill,
				Quantity:             3,
				CompanyID:            &companyID,
			}, nil
		},
		setCompanyBilling: func(ctx context.Context, id primitive.ObjectID, billing models.CompanyBilling) error {
			if id != companyID {
				t.Errorf("SetCompanyBilling(%s), want %s", id.Hex(), companyID.Hex())
			}
			billed = &billing
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			// The owner appears in the claimed list too (ensureOwnerMembership)
			// — the fan-out must dedupe, not recompute them twice.
			return []primitive.ObjectID{ownerID, memberID}, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			if appID != models.AppIDIndexly {
				t.Errorf("recompute app %q, want indexly", appID)
			}
			recomputed[uid]++
			return nil
		},
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionActivated, map[string]any{
		"id": "sub_team", "status": "active", "customer_id": "ctm_1",
		"items":       companySubItems(3),
		"custom_data": map[string]any{"userId": ownerID.Hex(), "companyId": companyID.Hex()},
		"current_billing_period": map[string]any{
			"starts_at": time.Now().UTC().Format(time.RFC3339),
			"ends_at":   validTill.Format(time.RFC3339),
		},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("company linkage happy path must ack, got: %v", err)
	}

	set := captured["$set"].(bson.M)
	if got, ok := set["company_id"].(primitive.ObjectID); !ok || got != companyID {
		t.Errorf("$set.company_id = %v, want %s", set["company_id"], companyID.Hex())
	}
	if set["quantity"] != 3 {
		t.Errorf("$set.quantity = %v, want 3", set["quantity"])
	}
	if billed == nil {
		t.Fatal("SetCompanyBilling must be called")
	}
	if billed.SeatsPurchased != 3 || billed.PaddleSubscriptionID != "sub_team" {
		t.Errorf("billing = %+v, want 3 seats on sub_team", billed)
	}
	if recomputed[ownerID] != 1 {
		t.Errorf("owner recomputed %d times, want exactly 1 (dedupe)", recomputed[ownerID])
	}
	if recomputed[memberID] != 1 {
		t.Errorf("member recomputed %d times, want 1", recomputed[memberID])
	}
}

func TestApplySubscription_CompanyLinkageNonOwnerRefused(t *testing.T) {
	buyerID := primitive.NewObjectID()
	realOwnerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()

	failed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: buyerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: realOwnerID}, nil
		},
		// applySubscription deliberately nil: applying a refused linkage panics.
		markFailed: func(ctx context.Context, eventID, errMsg string) error {
			failed = true
			return nil
		},
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_evil", "status": "active", "customer_id": "ctm_1",
		"items":       companySubItems(2),
		"custom_data": map[string]any{"companyId": companyID.Hex()},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("non-owner company linkage must fail the event (Paddle redelivers)")
	}
	if !failed {
		t.Error("refused linkage must be marked failed")
	}
}

func TestApplySubscription_CompanyLinkageUnknownCompanyRefused(t *testing.T) {
	buyerID := primitive.NewObjectID()
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: buyerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return false, nil, nil
		},
		markFailed: func(ctx context.Context, eventID, errMsg string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_ghost", "status": "active", "customer_id": "ctm_1",
		"items":       companySubItems(1),
		"custom_data": map[string]any{"companyId": primitive.NewObjectID().Hex()},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err == nil {
		t.Fatal("valid-but-unknown companyId must fail the event, never bill blind")
	}
}

func TestApplySubscription_GarbageCompanyIDFallsThroughToIndividual(t *testing.T) {
	userID := primitive.NewObjectID()
	var captured bson.M
	recomputed := false
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: userID}, nil
		},
		// findCompanyByID / setCompanyBilling / listClaimedUserIDs all nil:
		// any company-path call on a garbage companyId panics the test.
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed = true
			return nil
		},
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	armTrialLifecycle(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_1", "status": "active", "customer_id": "ctm_1", "items": subItems(),
		"custom_data": map[string]any{"companyId": "not-an-object-id"},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("garbage companyId must fall through to individual behavior, got: %v", err)
	}
	set := captured["$set"].(bson.M)
	if _, has := set["company_id"]; has {
		t.Error("garbage companyId must not stamp company_id")
	}
	if _, has := set["quantity"]; has {
		t.Error("garbage companyId must not stamp quantity")
	}
	if !recomputed {
		t.Error("individual recompute must still run")
	}
}

func TestApplySubscription_CompanyQuantityUpdateResyncsSeats(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	validTill := time.Now().UTC().Add(20 * 24 * time.Hour)

	var billed *models.CompanyBilling
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: ownerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: ownerID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				PaddleSubscriptionID: id, AppID: models.AppIDIndexly,
				Status: models.SubStatusActive, ValidTill: validTill,
				Quantity: 5, CompanyID: &companyID,
			}, nil
		},
		setCompanyBilling: func(ctx context.Context, id primitive.ObjectID, billing models.CompanyBilling) error {
			billed = &billing
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			return nil, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	// Seat decrease below SeatsUsed included: the sync just stamps the lower
	// number (decision 6) — no membership is touched by this path at all.
	env := subEnvelope(t, dto.EventSubscriptionUpdated, map[string]any{
		"id": "sub_team", "status": "active", "customer_id": "ctm_1",
		"items":       companySubItems(5),
		"custom_data": map[string]any{"companyId": companyID.Hex()},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("quantity update must ack, got: %v", err)
	}
	if billed == nil || billed.SeatsPurchased != 5 {
		t.Fatalf("billing = %+v, want seats re-synced to 5", billed)
	}
}

func TestApplySubscription_CompanyCancelZeroesSeats(t *testing.T) {
	ownerID := primitive.NewObjectID()
	memberID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()

	var billed *models.CompanyBilling
	recomputed := map[primitive.ObjectID]int{}
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: ownerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: ownerID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			return true, nil
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			// Post-apply state: canceled with valid_till clamped to the past.
			past := time.Now().UTC().Add(-time.Minute)
			return true, &models.Subscription{
				PaddleSubscriptionID: id, AppID: models.AppIDIndexly,
				Status: models.SubStatusCanceled, ValidTill: past,
				Quantity: 4, CompanyID: &companyID,
			}, nil
		},
		setCompanyBilling: func(ctx context.Context, id primitive.ObjectID, billing models.CompanyBilling) error {
			billed = &billing
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			return []primitive.ObjectID{memberID}, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed[uid]++
			return nil
		},
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCanceled, map[string]any{
		"id": "sub_team", "status": "canceled", "customer_id": "ctm_1",
		"items":       companySubItems(4),
		"custom_data": map[string]any{"companyId": companyID.Hex()},
		"canceled_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("company cancel must ack, got: %v", err)
	}
	if billed == nil || billed.SeatsPurchased != 0 {
		t.Fatalf("billing = %+v, want seats 0 after cancel", billed)
	}
	// Members are recomputed so their projection carries the terminal state;
	// access lapse at valid_till is the resolver's paid-through rule, free.
	if recomputed[memberID] != 1 || recomputed[ownerID] != 1 {
		t.Errorf("fan-out must recompute owner and member once each, got %v", recomputed)
	}
}

func TestApplySubscription_CompanyQuantityBelowOneTreatedAsOne(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	validTill := time.Now().UTC().Add(10 * 24 * time.Hour)

	var captured bson.M
	var billed *models.CompanyBilling
	m := &mockStore{
		findCustomer: func(ctx context.Context, id string) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: ownerID}, nil
		},
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: ownerID}, nil
		},
		applySubscription: func(ctx context.Context, id string, at time.Time, update bson.M) (bool, error) {
			captured = update
			return true, nil
		},
		findSubByPaddleID: func(ctx context.Context, id string) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				PaddleSubscriptionID: id, AppID: models.AppIDIndexly,
				Status: models.SubStatusActive, ValidTill: validTill,
				Quantity: 0, CompanyID: &companyID,
			}, nil
		},
		setCompanyBilling: func(ctx context.Context, id primitive.ObjectID, billing models.CompanyBilling) error {
			billed = &billing
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			return nil, nil
		},
		recompute:     func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
		markProcessed: func(ctx context.Context, eventID, note string) error { return nil },
	}
	freshInsert(m)
	s := newTestService(t, m)

	env := subEnvelope(t, dto.EventSubscriptionCreated, map[string]any{
		"id": "sub_team", "status": "active", "customer_id": "ctm_1",
		"items":       companySubItems(0),
		"custom_data": map[string]any{"companyId": companyID.Hex()},
	})
	if err := s.HandleWebhookEvent(context.Background(), env, []byte("{}")); err != nil {
		t.Fatalf("zero-quantity payload must ack, got: %v", err)
	}
	if set := captured["$set"].(bson.M); set["quantity"] != 1 {
		t.Errorf("$set.quantity = %v, want <1 clamped to 1", set["quantity"])
	}
	if billed == nil || billed.SeatsPurchased != 1 {
		t.Fatalf("billing = %+v, want seats clamped to 1 for a live sub", billed)
	}
}
