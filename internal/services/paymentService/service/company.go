package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Company (team seat) billing: ONE Paddle subscription on the company whose
// line-item quantity buys seats. The subscription doc is owner-keyed like any
// other but carries company_id, which keeps it out of every individual-flow
// query; this file is the only service surface that reads company subs.
// Everything here is fully operable with NO Paddle team product configured
// (decision 7): the checkout guard 400s first, the billing summary never
// calls Paddle, and cancel/resume 404 cleanly when no linked sub exists.

// loadOwnedTeamCompany resolves the company and enforces the shared guards:
// exists, caller owns it (D12 — billing is owner-only in-service regardless
// of the controller's permission gate).
func (s *service) loadOwnedTeamCompany(ctx context.Context, userID, companyID primitive.ObjectID) (*models.Company, error) {
	found, company, err := s.store.FindCompanyByID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCompanyNotFound
	}
	if company.OwnerUserID != userID {
		return nil, ErrNotCompanyOwner
	}
	return company, nil
}

// CreateCompanyCheckoutSession returns what the Paddle overlay needs for a
// per-seat team purchase (the FE opens it with quantity = seats and passes
// CustomData verbatim). Guard order matters: the unconfigured-product check
// comes FIRST — decision 7's single enforcement point — so a dark deploy
// answers a clean 400 before any DB or Paddle work.
func (s *service) CreateCompanyCheckoutSession(ctx context.Context, userID primitive.ObjectID, email string, companyID primitive.ObjectID) (*dto.CompanyCheckoutSessionResponse, error) {
	if s.teamProductID == "" {
		return nil, ErrTeamPlanNotAvailable
	}

	company, err := s.loadOwnedTeamCompany(ctx, userID, companyID)
	if err != nil {
		return nil, err
	}
	if company.Kind != models.CompanyKindTeam {
		return nil, ErrCompanyNotTeam
	}
	// D14: the website URL is the company's learn source — required before money.
	if strings.TrimSpace(company.WebsiteURL) == "" {
		return nil, ErrCompanyWebsiteRequired
	}

	// One live company subscription max — doc-keyed on company_id (the
	// owner's own personal sub must never block a team purchase, and vice
	// versa; that is the whole point of the company_id scoping).
	if found, sub, err := s.store.FindLatestSubscriptionByCompanyID(ctx, companyID); err != nil {
		return nil, err
	} else if found && time.Now().UTC().Before(sub.ValidTill) {
		return nil, ErrSubscriptionAlreadyActive
	}

	paddleCustomerID, err := s.ensurePaddleCustomer(ctx, userID, email)
	if err != nil {
		return nil, err
	}

	prices, err := s.paddle.ListActivePrices(ctx, s.teamProductID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
	}
	// Catalog-mapped prices only (the webhook fails closed on unmapped ones);
	// NO trial/no-trial split — the team plan has no trial (decision 2).
	offered := s.offeredPrices(prices)
	priceIDs := make([]string, 0, len(offered))
	for _, p := range offered {
		priceIDs = append(priceIDs, p.ID)
	}

	return &dto.CompanyCheckoutSessionResponse{
		PaddleCustomerID: paddleCustomerID,
		PriceIDs:         priceIDs,
		CustomData: map[string]string{
			"userId":    userID.Hex(),
			"companyId": companyID.Hex(),
		},
	}, nil
}

// GetCompanyBilling builds the billing summary from the company doc and its
// linked subscription doc. ZERO Paddle calls ever (decision 7): an unbilled
// company returns {seatsPurchased: 0, seatsUsed: N} with no subscription
// block — a valid 200, not an error.
func (s *service) GetCompanyBilling(ctx context.Context, companyID primitive.ObjectID) (*dto.CompanyBillingResponse, error) {
	found, company, err := s.store.FindCompanyByID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCompanyNotFound
	}

	resp := &dto.CompanyBillingResponse{
		SeatsPurchased: company.Billing.SeatsPurchased,
		SeatsUsed:      company.SeatsUsed,
	}

	subFound, sub, err := s.store.FindLatestSubscriptionByCompanyID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if subFound {
		now := time.Now().UTC()
		isValid := now.Before(sub.ValidTill)
		status := sub.Status
		if !isValid {
			// Same derivation rule as the individual status endpoint: the UI
			// must agree with the paid-through gate even when a renewal
			// webhook was missed.
			status = "expired"
		}
		seats := sub.Quantity
		if seats < 1 {
			seats = 1
		}
		resp.Subscription = &dto.CompanySubscriptionDTO{
			Status:            status,
			ValidTill:         sub.ValidTill,
			ScheduledCancelAt: sub.ScheduledCancelAt,
			IsValid:           isValid,
			Seats:             seats,
		}
	}
	return resp, nil
}

// CancelCompanySubscription schedules the company subscription's cancellation
// at period end (owner-only). Seats stay open until the confirming webhook
// zeroes them at the terminal event; nobody is auto-archived (decision 6).
// No linked live subscription → ErrNoActiveSubscription (clean 404, no
// Paddle call).
func (s *service) CancelCompanySubscription(ctx context.Context, userID, companyID primitive.ObjectID) (*dto.CancelResponse, error) {
	if _, err := s.loadOwnedTeamCompany(ctx, userID, companyID); err != nil {
		return nil, err
	}
	found, sub, err := s.store.FindLatestSubscriptionByCompanyID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if !found || !time.Now().UTC().Before(sub.ValidTill) {
		return nil, ErrNoActiveSubscription
	}

	// Already canceling — idempotent no-op, mirroring the individual flow.
	if sub.Status == models.SubStatusCanceling {
		return &dto.CancelResponse{Status: sub.Status, ValidTill: sub.ValidTill, ScheduledCancelAt: sub.ScheduledCancelAt}, nil
	}

	res, err := s.paddle.CancelAtPeriodEnd(ctx, sub.PaddleSubscriptionID)
	if err != nil {
		// Stale-local recovery, same as the individual cancel: if Paddle
		// already holds the scheduled cancellation, treat as success.
		cur, gerr := s.paddle.GetSubscription(ctx, sub.PaddleSubscriptionID)
		if gerr != nil || cur.ScheduledCancelAt == nil {
			return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		res = cur
	}
	scheduledAt := res.ScheduledCancelAt
	if scheduledAt == nil {
		scheduledAt = &sub.ValidTill
	}

	// Optimistic write — last_event_at untouched; the confirming webhook's
	// company sync (seats + fan-out) stays authoritative.
	if err := s.store.SetSubscriptionCanceling(ctx, sub.PaddleSubscriptionID, scheduledAt); err != nil {
		log.Error("optimistic company canceling write failed; webhook will reconcile",
			"error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
	s.recomputeCompanyAfterLocalWrite(ctx, companyID, userID)

	return &dto.CancelResponse{
		Status:            models.SubStatusCanceling,
		ValidTill:         sub.ValidTill,
		ScheduledCancelAt: scheduledAt,
	}, nil
}

// ResumeCompanySubscription un-cancels a still-valid canceling company
// subscription (owner-only). A fully lapsed subscription directs the FE
// through a fresh checkout; no linked sub at all → ErrNoActiveSubscription
// (clean 404, no Paddle call).
func (s *service) ResumeCompanySubscription(ctx context.Context, userID, companyID primitive.ObjectID) (*dto.ResubscribeResponse, error) {
	if _, err := s.loadOwnedTeamCompany(ctx, userID, companyID); err != nil {
		return nil, err
	}
	found, sub, err := s.store.FindLatestSubscriptionByCompanyID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNoActiveSubscription
	}
	if !time.Now().UTC().Before(sub.ValidTill) {
		return &dto.ResubscribeResponse{Mode: dto.ResubscribeModeCheckoutRequired}, nil
	}
	if sub.Status != models.SubStatusCanceling {
		return nil, ErrSubscriptionAlreadyActive
	}

	if _, err := s.paddle.RemoveScheduledCancellation(ctx, sub.PaddleSubscriptionID); err != nil {
		cur, gerr := s.paddle.GetSubscription(ctx, sub.PaddleSubscriptionID)
		switch {
		case gerr == nil && cur.Status == paddleStatusCanceled:
			return &dto.ResubscribeResponse{Mode: dto.ResubscribeModeCheckoutRequired}, nil
		case gerr == nil && cur.Status == paddleStatusActive && cur.ScheduledCancelAt == nil:
			// already un-canceled (idempotent retry) — proceed
		default:
			return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
	}

	if err := s.store.SetSubscriptionActive(ctx, sub.PaddleSubscriptionID); err != nil {
		log.Error("optimistic company un-cancel write failed; webhook will reconcile",
			"error", err, "paddle_subscription_id", sub.PaddleSubscriptionID)
	}
	s.recomputeCompanyAfterLocalWrite(ctx, companyID, userID)

	return &dto.ResubscribeResponse{
		Mode:      dto.ResubscribeModeUncanceled,
		ValidTill: &sub.ValidTill,
	}, nil
}

// recomputeCompanyAfterLocalWrite fans the entitlement recompute out to the
// owner and every claimed member after an optimistic company-sub write — the
// company counterpart of recomputeAfterLocalWrite, with the same best-effort
// contract: failures are logged, the confirming webhook's own fan-out heals.
func (s *service) recomputeCompanyAfterLocalWrite(ctx context.Context, companyID, ownerID primitive.ObjectID) {
	memberIDs, err := s.store.ListClaimedMembershipUserIDs(ctx, companyID)
	if err != nil {
		log.Error("company member list for recompute failed; webhook will reconcile",
			"error", err, "company_id", companyID.Hex())
		memberIDs = nil
	}
	recomputed := map[primitive.ObjectID]bool{}
	for _, uid := range append([]primitive.ObjectID{ownerID}, memberIDs...) {
		if recomputed[uid] {
			continue
		}
		recomputed[uid] = true
		if err := s.store.RecomputeUserEntitlement(ctx, uid, models.AppIDIndexly); err != nil {
			log.Error("entitlement recompute after company local write failed; webhook will reconcile",
				"error", err, "user_id", uid.Hex(), "company_id", companyID.Hex())
		}
	}
}
