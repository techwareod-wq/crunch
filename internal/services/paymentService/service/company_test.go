package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	appdto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/store"
)

// Company (team seat) billing tests. The mock panic convention carries the
// decision-7 assertions: an un-armed mockStore/mockPaddle proves a code path
// made ZERO DB/Paddle calls.

func newCompanyTestService(t *testing.T, st store.Store, paddle *mockPaddle, teamProductID string) *service {
	t.Helper()
	cache, err := entitlements.NewPlansCacheFromPlans([]models.Plan{
		{
			AppID: models.AppIDIndexly, Tier: "pro", Name: "Pro", Active: true,
			Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_1"}},
			Features: []string{"article.generate"},
		},
		{
			AppID: models.AppIDIndexly, Tier: entitlements.TierTeam, Name: "Team", Active: true,
			Variants: map[string]models.PlanVariant{"monthly": {PriceID: "pri_team"}},
			Features: []string{"article.generate"},
		},
	})
	if err != nil {
		t.Fatalf("plans cache: %v", err)
	}
	return &service{store: st, paddle: paddle, plans: cache, dispatcher: &mockDispatcher{},
		productID: "pro_test", teamProductID: teamProductID}
}

func teamCompany(id, ownerID primitive.ObjectID) *models.Company {
	return &models.Company{
		ID: id, Kind: models.CompanyKindTeam, OwnerUserID: ownerID,
		WebsiteURL: "https://example.com",
	}
}

// Decision 7's single enforcement point: with no team product configured the
// checkout answers ErrTeamPlanNotAvailable BEFORE any DB or Paddle work —
// both mocks are entirely un-armed, so any call panics the test.
func TestCompanyCheckout_UnconfiguredProductFailsClosedWithZeroCalls(t *testing.T) {
	s := newCompanyTestService(t, &mockStore{}, &mockPaddle{}, "")
	_, err := s.CreateCompanyCheckoutSession(context.Background(), primitive.NewObjectID(), "o@x.com", primitive.NewObjectID())
	if !errors.Is(err, ErrTeamPlanNotAvailable) {
		t.Fatalf("want ErrTeamPlanNotAvailable, got %v", err)
	}
}

func TestCompanyCheckout_GuardOrder(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()

	cases := []struct {
		name    string
		caller  primitive.ObjectID
		company *models.Company
		found   bool
		wantErr error
	}{
		{"unknown company", ownerID, nil, false, ErrCompanyNotFound},
		{"non-owner", primitive.NewObjectID(), teamCompany(companyID, ownerID), true, ErrNotCompanyOwner},
		{"personal company", ownerID, &models.Company{ID: companyID, Kind: models.CompanyKindPersonal, OwnerUserID: ownerID}, true, ErrCompanyNotTeam},
		{"no website (D14)", ownerID, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, OwnerUserID: ownerID}, true, ErrCompanyWebsiteRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockStore{
				findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
					return tc.found, tc.company, nil
				},
			}
			// Paddle un-armed: every guard rejection must precede Paddle work.
			s := newCompanyTestService(t, m, &mockPaddle{}, "pro_team")
			_, err := s.CreateCompanyCheckoutSession(context.Background(), tc.caller, "o@x.com", companyID)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCompanyCheckout_LiveCompanySubBlocks(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, teamCompany(companyID, ownerID), nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, &models.Subscription{ValidTill: time.Now().UTC().Add(time.Hour)}, nil
		},
	}
	s := newCompanyTestService(t, m, &mockPaddle{}, "pro_team")
	_, err := s.CreateCompanyCheckoutSession(context.Background(), ownerID, "o@x.com", companyID)
	if !errors.Is(err, ErrSubscriptionAlreadyActive) {
		t.Fatalf("want ErrSubscriptionAlreadyActive, got %v", err)
	}
}

func TestCompanyCheckout_HappyPathOffersCatalogMappedPricesAndCustomData(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, teamCompany(companyID, ownerID), nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return false, nil, nil
		},
		findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
			return true, &models.PaddleCustomer{UserID: uid, PaddleCustomerID: "ctm_owner"}, nil
		},
	}
	p := &mockPaddle{
		listActivePrices: func(ctx context.Context, productID string) ([]appdto.PaddlePrice, error) {
			if productID != "pro_team" {
				t.Errorf("ListActivePrices(%q), want pro_team", productID)
			}
			return []appdto.PaddlePrice{
				{ID: "pri_team", ProductID: "pro_team"},
				{ID: "pri_unmapped", ProductID: "pro_team"}, // live in Paddle, not in catalog — never offered
			}, nil
		},
	}
	s := newCompanyTestService(t, m, p, "pro_team")

	resp, err := s.CreateCompanyCheckoutSession(context.Background(), ownerID, "o@x.com", companyID)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if resp.PaddleCustomerID != "ctm_owner" {
		t.Errorf("customer = %q, want ctm_owner", resp.PaddleCustomerID)
	}
	if len(resp.PriceIDs) != 1 || resp.PriceIDs[0] != "pri_team" {
		t.Errorf("priceIds = %v, want exactly the catalog-mapped [pri_team]", resp.PriceIDs)
	}
	if resp.CustomData["userId"] != ownerID.Hex() || resp.CustomData["companyId"] != companyID.Hex() {
		t.Errorf("customData = %v, want userId+companyId", resp.CustomData)
	}
}

// The unbilled company answers a valid 200 with zeros and NO Paddle calls
// ever (decision 7): the paddle mock is fully un-armed.
func TestGetCompanyBilling_UnbilledCompanyIsZeros(t *testing.T) {
	companyID := primitive.NewObjectID()
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, &models.Company{ID: companyID, Kind: models.CompanyKindTeam, SeatsUsed: 2}, nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return false, nil, nil
		},
	}
	s := newCompanyTestService(t, m, &mockPaddle{}, "")

	resp, err := s.GetCompanyBilling(context.Background(), companyID)
	if err != nil {
		t.Fatalf("unbilled billing read: %v", err)
	}
	if resp.SeatsPurchased != 0 || resp.SeatsUsed != 2 || resp.Subscription != nil {
		t.Errorf("resp = %+v, want {0, 2, no subscription}", resp)
	}
}

func TestGetCompanyBilling_BilledAndExpiredDerivation(t *testing.T) {
	companyID := primitive.NewObjectID()
	future := time.Now().UTC().Add(10 * 24 * time.Hour)
	past := time.Now().UTC().Add(-time.Hour)

	mk := func(validTill time.Time, status string) *mockStore {
		return &mockStore{
			findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
				return true, &models.Company{
					ID: companyID, Kind: models.CompanyKindTeam, SeatsUsed: 1,
					Billing: models.CompanyBilling{PaddleSubscriptionID: "sub_team", SeatsPurchased: 3},
				}, nil
			},
			findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
				return true, &models.Subscription{
					PaddleSubscriptionID: "sub_team", Status: status, ValidTill: validTill, Quantity: 3,
				}, nil
			},
		}
	}

	s := newCompanyTestService(t, mk(future, models.SubStatusActive), &mockPaddle{}, "")
	resp, err := s.GetCompanyBilling(context.Background(), companyID)
	if err != nil {
		t.Fatalf("billed read: %v", err)
	}
	if resp.SeatsPurchased != 3 || resp.Subscription == nil || !resp.Subscription.IsValid ||
		resp.Subscription.Status != models.SubStatusActive || resp.Subscription.Seats != 3 {
		t.Errorf("live resp = %+v / %+v", resp, resp.Subscription)
	}

	s = newCompanyTestService(t, mk(past, models.SubStatusActive), &mockPaddle{}, "")
	resp, err = s.GetCompanyBilling(context.Background(), companyID)
	if err != nil {
		t.Fatalf("lapsed read: %v", err)
	}
	if resp.Subscription == nil || resp.Subscription.Status != "expired" || resp.Subscription.IsValid {
		t.Errorf("lapsed sub must derive expired, got %+v", resp.Subscription)
	}
}

// Cancel/resume with no linked subscription: clean sentinel (controller maps
// to 404), Paddle un-armed = no call attempted.
func TestCompanyCancelResume_NoLinkedSubIs404(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, teamCompany(companyID, ownerID), nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return false, nil, nil
		},
	}
	s := newCompanyTestService(t, m, &mockPaddle{}, "")

	if _, err := s.CancelCompanySubscription(context.Background(), ownerID, companyID); !errors.Is(err, ErrNoActiveSubscription) {
		t.Fatalf("cancel: want ErrNoActiveSubscription, got %v", err)
	}
	if _, err := s.ResumeCompanySubscription(context.Background(), ownerID, companyID); !errors.Is(err, ErrNoActiveSubscription) {
		t.Fatalf("resume: want ErrNoActiveSubscription, got %v", err)
	}
}

func TestCompanyCancel_OwnerOnlySchedulesAndFansOut(t *testing.T) {
	ownerID := primitive.NewObjectID()
	memberID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	validTill := time.Now().UTC().Add(15 * 24 * time.Hour)
	scheduled := validTill

	recomputed := map[primitive.ObjectID]int{}
	canceling := false
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, teamCompany(companyID, ownerID), nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				PaddleSubscriptionID: "sub_team", Status: models.SubStatusActive,
				ValidTill: validTill, Quantity: 3, CompanyID: &companyID, UserID: ownerID,
			}, nil
		},
		setCanceling: func(ctx context.Context, id string, at *time.Time) error {
			canceling = true
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			return []primitive.ObjectID{ownerID, memberID}, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error {
			recomputed[uid]++
			return nil
		},
	}
	p := &mockPaddle{
		cancelAtPeriodEnd: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
			return &appdto.PaddleSubscription{ID: id, Status: "active", ScheduledCancelAt: &scheduled}, nil
		},
	}
	s := newCompanyTestService(t, m, p, "pro_team")

	// Non-owner first: refused before any Paddle work (mock would panic).
	if _, err := s.CancelCompanySubscription(context.Background(), memberID, companyID); !errors.Is(err, ErrNotCompanyOwner) {
		t.Fatalf("non-owner cancel: want ErrNotCompanyOwner, got %v", err)
	}

	resp, err := s.CancelCompanySubscription(context.Background(), ownerID, companyID)
	if err != nil {
		t.Fatalf("owner cancel: %v", err)
	}
	if resp.Status != models.SubStatusCanceling || !canceling {
		t.Errorf("cancel must schedule canceling, resp=%+v canceling=%v", resp, canceling)
	}
	if recomputed[ownerID] != 1 || recomputed[memberID] != 1 {
		t.Errorf("fan-out recompute owner+member once each, got %v", recomputed)
	}
}

func TestCompanyResume_UncancelsStillValidSub(t *testing.T) {
	ownerID := primitive.NewObjectID()
	companyID := primitive.NewObjectID()
	validTill := time.Now().UTC().Add(5 * 24 * time.Hour)

	activated := false
	m := &mockStore{
		findCompanyByID: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Company, error) {
			return true, teamCompany(companyID, ownerID), nil
		},
		findLatestByComp: func(ctx context.Context, id primitive.ObjectID) (bool, *models.Subscription, error) {
			return true, &models.Subscription{
				PaddleSubscriptionID: "sub_team", Status: models.SubStatusCanceling,
				ValidTill: validTill, CompanyID: &companyID, UserID: ownerID,
			}, nil
		},
		setActive: func(ctx context.Context, id string) error {
			activated = true
			return nil
		},
		listClaimedUserIDs: func(ctx context.Context, id primitive.ObjectID) ([]primitive.ObjectID, error) {
			return nil, nil
		},
		recompute: func(ctx context.Context, uid primitive.ObjectID, appID string) error { return nil },
	}
	p := &mockPaddle{
		removeScheduled: func(ctx context.Context, id string) (*appdto.PaddleSubscription, error) {
			return &appdto.PaddleSubscription{ID: id, Status: "active"}, nil
		},
	}
	s := newCompanyTestService(t, m, p, "pro_team")

	resp, err := s.ResumeCompanySubscription(context.Background(), ownerID, companyID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resp.Mode != "uncanceled" || !activated {
		t.Errorf("resume must uncancel, resp=%+v activated=%v", resp, activated)
	}
}

// The Paddle-side mirror of the company_id scoping: the owner's live TEAM
// subscription must not block their PERSONAL checkout through the webhook-lag
// guard, while any other live sub still does.
func TestPersonalCheckout_LagGuardIgnoresTeamProductSubs(t *testing.T) {
	userID := primitive.NewObjectID()
	baseStore := func(liveTeamOnly bool) *mockStore {
		return &mockStore{
			findLatestByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.Subscription, error) {
				return false, nil, nil
			},
			findCustomerByUser: func(ctx context.Context, uid primitive.ObjectID) (bool, *models.PaddleCustomer, error) {
				return true, &models.PaddleCustomer{UserID: uid, PaddleCustomerID: "ctm_1"}, nil
			},
			trialEligible: func(ctx context.Context, uid primitive.ObjectID, cid, ex string) (bool, error) {
				return false, nil
			},
		}
	}

	// Only a team-product sub live → personal checkout proceeds.
	p := &mockPaddle{
		listLiveSubs: func(ctx context.Context, customerID string) ([]appdto.PaddleSubscription, error) {
			return []appdto.PaddleSubscription{{ID: "sub_team", ProductID: "pro_team", Status: "active"}}, nil
		},
		listActivePrices: func(ctx context.Context, productID string) ([]appdto.PaddlePrice, error) {
			return []appdto.PaddlePrice{{ID: "pri_1", ProductID: "pro_test"}}, nil
		},
	}
	s := newCompanyTestService(t, baseStore(true), p, "pro_team")
	if _, err := s.CreateCheckoutSession(context.Background(), userID, "o@x.com"); err != nil {
		t.Fatalf("team-product live sub must not block personal checkout, got %v", err)
	}

	// A live individual sub still blocks.
	p.listLiveSubs = func(ctx context.Context, customerID string) ([]appdto.PaddleSubscription, error) {
		return []appdto.PaddleSubscription{{ID: "sub_ind", ProductID: "pro_test", Status: "active"}}, nil
	}
	s = newCompanyTestService(t, baseStore(false), p, "pro_team")
	if _, err := s.CreateCheckoutSession(context.Background(), userID, "o@x.com"); !errors.Is(err, ErrSubscriptionAlreadyActive) {
		t.Fatalf("live individual sub must still block, got %v", err)
	}
}
