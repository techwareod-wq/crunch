package dto

import (
	"encoding/json"
	"time"
)

// Paddle webhook event types consumed by the service.
const (
	EventCustomerCreated          = "customer.created"
	EventCustomerUpdated          = "customer.updated"
	EventSubscriptionCreated      = "subscription.created"
	EventSubscriptionActivated    = "subscription.activated"
	EventSubscriptionTrialing     = "subscription.trialing"
	EventSubscriptionUpdated      = "subscription.updated"
	EventSubscriptionCanceled     = "subscription.canceled"
	EventSubscriptionPastDue      = "subscription.past_due"
	EventSubscriptionPaused       = "subscription.paused"
	EventSubscriptionResumed      = "subscription.resumed"
	EventTransactionCompleted     = "transaction.completed"
	EventTransactionPaymentFailed = "transaction.payment_failed"
)

// WebhookEnvelope is the minimal top-level shape of every Paddle webhook;
// Data is parsed per event type (mirrors the Clerk handler's envelope style).
type WebhookEnvelope struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

type WebhookTimePeriod struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

type WebhookScheduledChange struct {
	Action      string    `json:"action"`
	EffectiveAt time.Time `json:"effective_at"`
}

type WebhookSubscriptionItem struct {
	Price struct {
		ID        string `json:"id"`
		ProductID string `json:"product_id"`
	} `json:"price"`
	// Quantity is the seat count on team (company-linked) subscriptions;
	// <1 is treated as 1 at apply time.
	Quantity int `json:"quantity"`
}

// WebhookSubscription is the subset of subscription.* payloads the service
// applies. Webhook handlers recompute full state from this payload, never
// from deltas.
type WebhookSubscription struct {
	ID                   string                    `json:"id"`
	Status               string                    `json:"status"`
	CustomerID           string                    `json:"customer_id"`
	CustomData           map[string]any            `json:"custom_data"`
	CurrentBillingPeriod *WebhookTimePeriod        `json:"current_billing_period"` // null for paused/canceled
	ScheduledChange      *WebhookScheduledChange   `json:"scheduled_change"`
	StartedAt            *time.Time                `json:"started_at"`
	CanceledAt           *time.Time                `json:"canceled_at"`
	Items                []WebhookSubscriptionItem `json:"items"`
}

// WebhookTransaction is the subset of transaction.* payloads the service records.
type WebhookTransaction struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	CustomerID     string         `json:"customer_id"`
	SubscriptionID string         `json:"subscription_id"`
	CustomData     map[string]any `json:"custom_data"`
	CurrencyCode   string         `json:"currency_code"`
	BilledAt       *time.Time     `json:"billed_at"`
	InvoiceNumber  string         `json:"invoice_number"`
	Details        struct {
		Totals struct {
			Total string `json:"total"`
		} `json:"totals"`
	} `json:"details"`
}

// WebhookCustomer is the subset of customer.* payloads the service records.
type WebhookCustomer struct {
	ID         string         `json:"id"`
	Email      string         `json:"email"`
	CustomData map[string]any `json:"custom_data"`
}
