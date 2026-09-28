package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// CreateCheckoutSession ensures a Paddle customer exists for the user and
// returns what the Paddle.js overlay needs. Rejects when a valid subscription
// already exists (locally, or live in Paddle during the webhook-lag window).
func (s *service) CreateCheckoutSession(ctx context.Context, userID primitive.ObjectID, email string) (*dto.CheckoutSessionResponse, error) {
	// A valid PAID subscription blocks a second checkout. A card-less local
	// trial does NOT: upgrading to paid mid-trial is the intended path (and the
	// point of the trial). The webhook supersedes the trial when the paid
	// subscription activates.
	found, latest, err := s.store.FindLatestSubscriptionByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if found && time.Now().UTC().Before(latest.ValidTill) && !latest.IsLocalTrial() {
		return nil, ErrSubscriptionAlreadyActive
	}

	paddleCustomerID, err := s.ensurePaddleCustomer(ctx, userID, email)
	if err != nil {
		return nil, err
	}

	// Webhook-lag guard: the user may have just paid (no Mongo doc yet) —
	// ask Paddle directly before allowing a second checkout that would
	// create a second subscription and double-bill. Team-product subs are
	// ignored: the owner's company seat subscription lives under the same
	// Paddle customer and must never block their PERSONAL checkout (the
	// Paddle-side mirror of the company_id query scoping). While no team
	// product is configured the filter matches nothing and behavior is
	// byte-for-byte the pre-team-billing one.
	liveSubs, err := s.paddle.ListLiveSubscriptionsByCustomer(ctx, paddleCustomerID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
	}
	for _, live := range liveSubs {
		if s.teamProductID != "" && live.ProductID == s.teamProductID {
			continue
		}
		return nil, ErrSubscriptionAlreadyActive
	}

	prices, err := s.paddle.ListActivePrices(ctx, s.productID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentProvider, err)
	}

	// Offer only the eligibility-appropriate, catalog-mapped price so a returning
	// user can't open the overlay against the trial price and the overlay can
	// never open against a price the webhook would reject as unmapped (matches
	// GetPlans). Catalog filter first, then the trial/no-trial eligibility split.
	eligible, err := s.trialEligible(ctx, userID)
	if err != nil {
		return nil, err
	}
	selected := s.selectPricesForUser(s.offeredPrices(prices), eligible, userID)
	priceIDs := make([]string, 0, len(selected))
	for _, p := range selected {
		priceIDs = append(priceIDs, p.ID)
	}

	return &dto.CheckoutSessionResponse{
		PaddleCustomerID: paddleCustomerID,
		PriceIDs:         priceIDs,
	}, nil
}

func (s *service) ensurePaddleCustomer(ctx context.Context, userID primitive.ObjectID, email string) (string, error) {
	found, existing, err := s.store.FindCustomerByUserID(ctx, userID)
	if err != nil {
		return "", err
	}
	if found {
		return existing.PaddleCustomerID, nil
	}

	// JWT bootstrap can create users with an empty email (Clerk API blip);
	// Paddle rejects empty emails, so surface an actionable error instead
	// of an opaque 502.
	if email == "" {
		return "", ErrEmailRequired
	}

	customer, err := s.paddle.CreateCustomer(ctx, email, map[string]any{"userId": userID.Hex()})
	if err != nil {
		if !errors.Is(err, interfaces.ErrPaddleCustomerExists) {
			return "", fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
		// Paddle already knows this email (Mongo/Paddle drift) — adopt the
		// existing customer instead of failing checkout.
		customer, err = s.paddle.GetCustomerByEmail(ctx, email)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
	}

	if customer.Archived {
		if err := s.paddle.ReactivateCustomer(ctx, customer.ID); err != nil {
			return "", fmt.Errorf("%w: %v", ErrPaymentProvider, err)
		}
	}

	insertErr := s.store.InsertCustomer(ctx, &models.PaddleCustomer{
		UserID:           userID,
		PaddleCustomerID: customer.ID,
		Email:            customer.Email,
	})
	if insertErr != nil {
		if !mongo.IsDuplicateKeyError(insertErr) {
			return "", insertErr
		}
		// Concurrent checkout-session race: another request inserted the
		// mapping first. Use it. Paddle customers can't be deleted, so if we
		// created a second one it stays orphaned — log it.
		raceFound, raced, rerr := s.store.FindCustomerByUserID(ctx, userID)
		if rerr != nil || !raceFound {
			return "", insertErr
		}
		if raced.PaddleCustomerID != customer.ID {
			log.Warn("orphaned paddle customer from concurrent checkout race",
				"orphan_customer_id", customer.ID, "kept_customer_id", raced.PaddleCustomerID, "user_id", userID.Hex())
		}
		return raced.PaddleCustomerID, nil
	}

	return customer.ID, nil
}
