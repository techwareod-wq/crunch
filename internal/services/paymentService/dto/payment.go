package dto

import "time"

type PaymentStatusResponse struct {
	HasSubscription bool             `json:"hasSubscription"`
	Subscription    *SubscriptionDTO `json:"subscription,omitempty"` // legacy Indexly shape, kept
	// Apps is per-app access derived from the entitlement projection: one
	// entry per app in the union of the plan catalog and the user's
	// entitlements. The frontend gates UI from this; the server stays
	// authoritative.
	Apps map[string]AppAccessDTO `json:"apps"`
	// TrialUsage is the free-plan article meter — only present while the
	// subscription is a live card-less trial. The trial never lapses by date;
	// this is the number the banner renders ("X of N free articles left").
	TrialUsage *TrialUsageDTO `json:"trialUsage,omitempty"`
}

// TrialUsageDTO mirrors the orchestrate cap check: ArticlesUsed is the user's
// monotonic lifetime generation counter (deletes don't refund), MaxArticles is
// values.yaml siteIntelligence.trial.maxArticles.
type TrialUsageDTO struct {
	ArticlesUsed int `json:"articlesUsed"`
	MaxArticles  int `json:"maxArticles"`
}

type AppAccessDTO struct {
	Status string `json:"status"` // none | trialing | active | canceling | past_due | ... | expired (derived)
	Tier   string `json:"tier,omitempty"`
	// ValidTill is a POINTER: a zero time.Time would never be omitted by
	// encoding/json, and "none" entries have no boundary at all.
	ValidTill *time.Time `json:"validTill,omitempty"`
	// Features is the EFFECTIVE set (plan list ± live overrides); [] for
	// none/expired.
	Features []string `json:"features"`
}

type SubscriptionDTO struct {
	Status            string     `json:"status"` // active | canceling | past_due | canceled | paused | expired
	PriceID           string     `json:"priceId"`
	ValidTill         time.Time  `json:"validTill"`
	ScheduledCancelAt *time.Time `json:"scheduledCancelAt,omitempty"`
	// IsValid is the single source the frontend keys on: now < validTill.
	IsValid bool `json:"isValid"`
	// IsLocalTrial marks a card-less local trial (source:"trial") — no Paddle
	// object, no payment method behind it. The frontend uses it to offer an
	// explicit "upgrade to paid" CTA during the trial: a card-less trial hard-
	// paywalls at trial end unless the user pays, whereas a Paddle card-required
	// trial auto-converts (and its overlay checkout would 409 as already active).
	IsLocalTrial bool `json:"isLocalTrial,omitempty"`
}

// UserPaymentStatusResponse is the client-facing projection of
// PaymentStatusResponse served by GET /v1/payments/status. It deliberately
// omits detail the app never reads and shouldn't see — the Paddle price id, the
// per-app tier, and the effective feature list — which stay on the full shape
// used by the admin status endpoint. The server remains authoritative; the app
// gates reactively (402 + isValid), not from these fields.
type UserPaymentStatusResponse struct {
	HasSubscription bool                        `json:"hasSubscription"`
	Subscription    *UserSubscriptionDTO        `json:"subscription,omitempty"`
	Apps            map[string]UserAppAccessDTO `json:"apps"`
	// TrialUsage is the free-plan article meter — see PaymentStatusResponse.
	TrialUsage *TrialUsageDTO `json:"trialUsage,omitempty"`
}

type UserSubscriptionDTO struct {
	Status            string     `json:"status"`
	ValidTill         time.Time  `json:"validTill"`
	ScheduledCancelAt *time.Time `json:"scheduledCancelAt,omitempty"`
	// IsValid is the single signal the frontend keys on: now < validTill.
	IsValid bool `json:"isValid"`
	// IsLocalTrial marks a card-less local trial — see SubscriptionDTO. Drives
	// the in-trial "upgrade to Pro" CTA on the payments panel.
	IsLocalTrial bool `json:"isLocalTrial,omitempty"`
}

type UserAppAccessDTO struct {
	Status    string     `json:"status"` // none | trialing | active | canceling | past_due | expired
	ValidTill *time.Time `json:"validTill,omitempty"`
}

// ForUser projects the full (admin) status onto the lean client shape, dropping
// priceId, tier, and features. Pure over its receiver.
func (r *PaymentStatusResponse) ForUser() *UserPaymentStatusResponse {
	out := &UserPaymentStatusResponse{
		HasSubscription: r.HasSubscription,
		Apps:            make(map[string]UserAppAccessDTO, len(r.Apps)),
		TrialUsage:      r.TrialUsage,
	}
	if r.Subscription != nil {
		out.Subscription = &UserSubscriptionDTO{
			Status:            r.Subscription.Status,
			ValidTill:         r.Subscription.ValidTill,
			ScheduledCancelAt: r.Subscription.ScheduledCancelAt,
			IsValid:           r.Subscription.IsValid,
			IsLocalTrial:      r.Subscription.IsLocalTrial,
		}
	}
	for id, a := range r.Apps {
		out.Apps[id] = UserAppAccessDTO{Status: a.Status, ValidTill: a.ValidTill}
	}
	return out
}

type PlanDTO struct {
	PriceID          string `json:"priceId"`
	ProductID        string `json:"productId"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	UnitAmount       string `json:"unitAmount"` // minor units, e.g. "2900"
	CurrencyCode     string `json:"currencyCode"`
	BillingInterval  string `json:"billingInterval"` // day | week | month | year
	BillingFrequency int    `json:"billingFrequency"`
	TrialInterval    string `json:"trialInterval,omitempty"`
	TrialFrequency   int    `json:"trialFrequency,omitempty"`
	// CardlessTrialDays > 0 tells the frontend to offer "start free trial"
	// (POST /v1/payments/start-trial, no checkout) for this price. Only set
	// for trial-eligible users, mirroring how the trial price is offered.
	CardlessTrialDays int `json:"cardlessTrialDays,omitempty"`
	// CardlessTrialArticles is the free plan's lifetime article cap
	// (values.yaml siteIntelligence.trial.maxArticles) — the plan never
	// lapses by date, so the offer copy quotes articles, not days. Set
	// alongside CardlessTrialDays.
	CardlessTrialArticles int `json:"cardlessTrialArticles,omitempty"`
}

type StartTrialResponse struct {
	AppID     string    `json:"appId"`
	Status    string    `json:"status"` // always "trialing"
	ValidTill time.Time `json:"validTill"`
}

type CheckoutSessionResponse struct {
	PaddleCustomerID string   `json:"paddleCustomerId"`
	PriceIDs         []string `json:"priceIds"`
}

// CompanyCheckoutSessionResponse feeds the Paddle overlay for a team-seat
// purchase: the overlay opens with quantity = seats and passes CustomData
// verbatim, which is how the webhook links the subscription to the company
// ({userId, companyId} — the owner-verified linkage).
type CompanyCheckoutSessionResponse struct {
	PaddleCustomerID string            `json:"paddleCustomerId"`
	PriceIDs         []string          `json:"priceIds"`
	CustomData       map[string]string `json:"customData"`
}

// CompanyBillingResponse is the company billing summary — built from the
// company doc and its linked subscription doc ONLY, never a Paddle call. An
// unbilled company is a valid 200 with zero seats and no Subscription block.
type CompanyBillingResponse struct {
	SeatsPurchased int `json:"seatsPurchased"`
	SeatsUsed      int `json:"seatsUsed"`
	// Subscription is absent while the company has never been billed.
	Subscription *CompanySubscriptionDTO `json:"subscription,omitempty"`
}

type CompanySubscriptionDTO struct {
	Status            string     `json:"status"` // active | canceling | past_due | canceled | paused | expired (derived)
	ValidTill         time.Time  `json:"validTill"`
	ScheduledCancelAt *time.Time `json:"scheduledCancelAt,omitempty"`
	// IsValid mirrors the individual DTO: now < validTill.
	IsValid bool `json:"isValid"`
	// Seats is the Paddle line-item quantity backing seatsPurchased while the
	// subscription is live (seat changes happen as Paddle quantity updates).
	Seats int `json:"seats"`
}

type CancelResponse struct {
	Status            string     `json:"status"`
	ValidTill         time.Time  `json:"validTill"`
	ScheduledCancelAt *time.Time `json:"scheduledCancelAt,omitempty"`
}

const (
	ResubscribeModeUncanceled       = "uncanceled"
	ResubscribeModeCheckoutRequired = "checkout_required"
)

type ResubscribeResponse struct {
	Mode      string     `json:"mode"` // uncanceled | checkout_required
	ValidTill *time.Time `json:"validTill,omitempty"`
}

type TransactionDTO struct {
	PaddleTransactionID string     `json:"transactionId"`
	Status              string     `json:"status"` // completed | failed
	AmountTotal         string     `json:"amountTotal"`
	CurrencyCode        string     `json:"currencyCode"`
	BilledAt            *time.Time `json:"billedAt,omitempty"`
	InvoiceNumber       string     `json:"invoiceNumber,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}
