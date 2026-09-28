package paddle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	sdk "github.com/PaddleHQ/paddle-go-sdk/v4"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

var errNotConfigured = errors.New("paddle provider is not configured (PADDLE_API_KEY is empty)")

type provider struct {
	client      *sdk.SDK
	verifier    *sdk.WebhookVerifier
	environment string

	// apiCallTimeout bounds every outbound Paddle API call. The SDK has no
	// client timeout of its own and handler contexts carry no deadline, so a
	// hung Paddle API would otherwise pin goroutines indefinitely.
	apiCallTimeout time.Duration
	// priceCacheTTL is how long ListActivePrices results are served from
	// memory. Adding a plan in the Paddle dashboard shows up after at most
	// this long — no deploy needed.
	priceCacheTTL time.Duration

	priceCacheMu sync.Mutex
	priceCache   map[string]*priceCacheEntry
}

type priceCacheEntry struct {
	prices    []dto.PaddlePrice
	fetchedAt time.Time
}

// GetProvider constructs the Paddle client wrapper. With an empty apiKey it
// returns a provider whose API calls fail with a clear "not configured" error
// instead of opaque 401s — local dev without Paddle keys must still boot.
// Production rejects empty keys at startup (see ProvideAppContext).
//
// webhookTimestampTolerance bounds replay of captured webhook payloads — kept
// generous enough to absorb local clock drift during ngrok testing.
func GetProvider(apiKey, webhookSecret, environment string, apiCallTimeout, priceCacheTTL, webhookTimestampTolerance time.Duration) (interfaces.PaddleClient, error) {
	p := &provider{
		environment:    environment,
		apiCallTimeout: apiCallTimeout,
		priceCacheTTL:  priceCacheTTL,
		priceCache:     make(map[string]*priceCacheEntry),
	}

	if webhookSecret != "" {
		p.verifier = sdk.NewWebhookVerifier(webhookSecret, sdk.VerifierWithTimestampTolerance(webhookTimestampTolerance))
	}

	if apiKey == "" {
		log.Warn("paddle provider constructed without API key — payment API calls will fail", "environment", environment)
		return p, nil
	}

	// The SDK's default client has no timeout; per-call context deadlines
	// (apiCallTimeout) plus this transport-level cap bound every request.
	httpClient := &http.Client{Timeout: apiCallTimeout + 5*time.Second}

	var (
		client *sdk.SDK
		err    error
	)
	if environment == "production" {
		client, err = sdk.New(apiKey, sdk.WithClient(httpClient))
	} else {
		client, err = sdk.NewSandbox(apiKey, sdk.WithClient(httpClient))
	}
	if err != nil {
		return nil, fmt.Errorf("paddle sdk init (%s): %w", environment, err)
	}
	p.client = client

	return p, nil
}

func (p *provider) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, p.apiCallTimeout)
}

func (p *provider) CreateCustomer(ctx context.Context, email string, customData map[string]any) (*dto.PaddleCustomer, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.CreateCustomer(ctx, &sdk.CreateCustomerRequest{
		Email:      email,
		CustomData: sdk.CustomData(customData),
	})
	if err != nil {
		if errors.Is(err, sdk.ErrCustomerAlreadyExists) {
			return nil, fmt.Errorf("paddle create customer: %w", interfaces.ErrPaddleCustomerExists)
		}
		return nil, fmt.Errorf("paddle create customer: %w", err)
	}
	return &dto.PaddleCustomer{ID: res.ID, Email: res.Email}, nil
}

func (p *provider) GetCustomerByEmail(ctx context.Context, email string) (*dto.PaddleCustomer, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.ListCustomers(ctx, &sdk.ListCustomersRequest{
		Email:  []string{email},
		Status: []string{string(sdk.StatusActive), string(sdk.StatusArchived)},
	})
	if err != nil {
		return nil, fmt.Errorf("paddle list customers: %w", err)
	}

	var found *dto.PaddleCustomer
	if err := res.Iter(ctx, func(c *sdk.Customer) (bool, error) {
		found = &dto.PaddleCustomer{ID: c.ID, Email: c.Email, Archived: c.Status == sdk.StatusArchived}
		return false, nil // exact-match email filter: first hit is enough
	}); err != nil {
		return nil, fmt.Errorf("paddle list customers iterate: %w", err)
	}
	if found == nil {
		return nil, fmt.Errorf("paddle customer not found for email")
	}
	return found, nil
}

func (p *provider) ReactivateCustomer(ctx context.Context, customerID string) error {
	if p.client == nil {
		return errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	active := sdk.StatusActive
	_, err := p.client.UpdateCustomer(ctx, &sdk.UpdateCustomerRequest{
		CustomerID: customerID,
		Status:     sdk.NewPatchField(active),
	})
	if err != nil {
		return fmt.Errorf("paddle reactivate customer: %w", err)
	}
	return nil
}

// ListActivePrices serves from a TTL cache; concurrent refreshes are
// serialized by the mutex and a refresh failure after expiry serves the stale
// entry instead of erroring, so a Paddle blip never breaks the payments page.
func (p *provider) ListActivePrices(ctx context.Context, productID string) ([]dto.PaddlePrice, error) {
	p.priceCacheMu.Lock()
	defer p.priceCacheMu.Unlock()

	entry := p.priceCache[productID]
	if entry != nil && time.Since(entry.fetchedAt) < p.priceCacheTTL {
		return entry.prices, nil
	}

	prices, err := p.fetchActivePrices(ctx, productID)
	if err != nil {
		if entry != nil {
			log.Warn("paddle price refresh failed, serving stale cache", "error", err, "product_id", productID)
			return entry.prices, nil
		}
		return nil, err
	}

	p.priceCache[productID] = &priceCacheEntry{prices: prices, fetchedAt: time.Now()}
	return prices, nil
}

func (p *provider) fetchActivePrices(ctx context.Context, productID string) ([]dto.PaddlePrice, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	recurring := true
	res, err := p.client.ListPrices(ctx, &sdk.ListPricesRequest{
		ProductID: []string{productID},
		Status:    []string{string(sdk.StatusActive)},
		Recurring: &recurring,
	})
	if err != nil {
		return nil, fmt.Errorf("paddle list prices: %w", err)
	}

	var prices []dto.PaddlePrice
	if err := res.IterErr(ctx, func(pr *sdk.Price) error {
		prices = append(prices, mapPrice(pr))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("paddle list prices iterate: %w", err)
	}
	return prices, nil
}

func mapPrice(pr *sdk.Price) dto.PaddlePrice {
	out := dto.PaddlePrice{
		ID:           pr.ID,
		ProductID:    pr.ProductID,
		Description:  pr.Description,
		UnitAmount:   pr.UnitPrice.Amount,
		CurrencyCode: string(pr.UnitPrice.CurrencyCode),
	}
	if pr.Name != nil {
		out.Name = *pr.Name
	}
	if pr.BillingCycle != nil {
		out.BillingInterval = string(pr.BillingCycle.Interval)
		out.BillingFrequency = pr.BillingCycle.Frequency
	}
	if pr.TrialPeriod != nil {
		out.TrialInterval = string(pr.TrialPeriod.Interval)
		out.TrialFrequency = pr.TrialPeriod.Frequency
	}
	return out
}

func (p *provider) GetSubscription(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.GetSubscription(ctx, &sdk.GetSubscriptionRequest{SubscriptionID: subscriptionID})
	if err != nil {
		return nil, fmt.Errorf("paddle get subscription: %w", err)
	}
	return mapSubscription(res), nil
}

func (p *provider) ListLiveSubscriptionsByCustomer(ctx context.Context, customerID string) ([]dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.ListSubscriptions(ctx, &sdk.ListSubscriptionsRequest{
		CustomerID: []string{customerID},
		Status:     []string{"active", "trialing", "past_due"},
	})
	if err != nil {
		return nil, fmt.Errorf("paddle list subscriptions: %w", err)
	}

	var subs []dto.PaddleSubscription
	if err := res.IterErr(ctx, func(s *sdk.Subscription) error {
		subs = append(subs, *mapSubscription(s))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("paddle list subscriptions iterate: %w", err)
	}
	return subs, nil
}

func (p *provider) CancelAtPeriodEnd(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	effectiveFrom := sdk.EffectiveFromNextBillingPeriod
	res, err := p.client.CancelSubscription(ctx, &sdk.CancelSubscriptionRequest{
		SubscriptionID: subscriptionID,
		EffectiveFrom:  &effectiveFrom,
	})
	if err != nil {
		return nil, fmt.Errorf("paddle cancel subscription: %w", err)
	}
	return mapSubscription(res), nil
}

func (p *provider) CancelImmediately(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	effectiveFrom := sdk.EffectiveFromImmediately
	res, err := p.client.CancelSubscription(ctx, &sdk.CancelSubscriptionRequest{
		SubscriptionID: subscriptionID,
		EffectiveFrom:  &effectiveFrom,
	})
	if err != nil {
		return nil, fmt.Errorf("paddle cancel subscription immediately: %w", err)
	}
	return mapSubscription(res), nil
}

func (p *provider) RemoveScheduledCancellation(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	// Paddle only accepts an explicit JSON null for scheduled_change — the
	// field must be present in the PATCH body, not omitted.
	res, err := p.client.UpdateSubscription(ctx, &sdk.UpdateSubscriptionRequest{
		SubscriptionID:  subscriptionID,
		ScheduledChange: sdk.NewNullPatchField[*sdk.SubscriptionScheduledChange](),
	})
	if err != nil {
		return nil, fmt.Errorf("paddle remove scheduled cancellation: %w", err)
	}
	return mapSubscription(res), nil
}

func (p *provider) SetNextBilledAt(ctx context.Context, subscriptionID string, at time.Time) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.UpdateSubscription(ctx, &sdk.UpdateSubscriptionRequest{
		SubscriptionID: subscriptionID,
		NextBilledAt:   sdk.NewPatchField(at.UTC().Format(time.RFC3339)),
	})
	if err != nil {
		return nil, fmt.Errorf("paddle set next billed at: %w", err)
	}
	return mapSubscription(res), nil
}

func (p *provider) ActivateTrialingSubscription(ctx context.Context, subscriptionID string) (*dto.PaddleSubscription, error) {
	if p.client == nil {
		return nil, errNotConfigured
	}
	ctx, cancel := p.callCtx(ctx)
	defer cancel()

	res, err := p.client.ActivateSubscription(ctx, &sdk.ActivateSubscriptionRequest{SubscriptionID: subscriptionID})
	if err != nil {
		return nil, fmt.Errorf("paddle activate trialing subscription: %w", err)
	}
	return mapSubscription(res), nil
}

func mapSubscription(s *sdk.Subscription) *dto.PaddleSubscription {
	out := &dto.PaddleSubscription{
		ID:         s.ID,
		Status:     string(s.Status),
		CustomerID: s.CustomerID,
	}
	if len(s.Items) > 0 {
		out.PriceID = s.Items[0].Price.ID
		out.ProductID = s.Items[0].Price.ProductID
	}
	// CurrentBillingPeriod is genuinely nil for paused and canceled subs.
	if s.CurrentBillingPeriod != nil {
		out.CurrentPeriodEndsAt = parseRFC3339Ptr(s.CurrentBillingPeriod.EndsAt)
	}
	if s.ScheduledChange != nil && s.ScheduledChange.Action == sdk.ScheduledChangeActionCancel {
		out.ScheduledCancelAt = parseRFC3339Ptr(s.ScheduledChange.EffectiveAt)
	}
	if s.CanceledAt != nil {
		out.CanceledAt = parseRFC3339Ptr(*s.CanceledAt)
	}
	return out
}

func parseRFC3339Ptr(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// VerifyWebhook validates the Paddle-Signature header against rawBody.
//
// The SDK verifier reads req.Body itself; by the time the handler calls this,
// the body has already been drained by io.ReadAll. Restore it from rawBody
// first or every webhook fails verification with an HMAC over an empty body.
func (p *provider) VerifyWebhook(req *http.Request, rawBody []byte) error {
	if p.verifier == nil {
		return fmt.Errorf("paddle webhook verifier is not configured (environment=%s)", p.environment)
	}

	req.Body = io.NopCloser(bytes.NewReader(rawBody))
	ok, err := p.verifier.Verify(req)
	// Verify re-buffers the body, but restore it deterministically anyway.
	req.Body = io.NopCloser(bytes.NewReader(rawBody))

	if err != nil {
		return fmt.Errorf("paddle webhook verification (environment=%s): %w", p.environment, err)
	}
	if !ok {
		return fmt.Errorf("paddle webhook signature mismatch (environment=%s)", p.environment)
	}
	return nil
}
