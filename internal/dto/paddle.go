package dto

import "time"

// PaddleCustomer is the subset of a Paddle customer entity the app consumes.
type PaddleCustomer struct {
	ID       string // "ctm_..."
	Email    string
	Archived bool
}

// PaddlePrice is the subset of a Paddle price entity rendered as a plan card.
type PaddlePrice struct {
	ID               string // "pri_..."
	ProductID        string // "pro_..."
	Name             string // customer-facing name shown at checkout
	Description      string
	UnitAmount       string // minor units, e.g. "2900"
	CurrencyCode     string
	BillingInterval  string // "day" | "week" | "month" | "year"
	BillingFrequency int
	TrialInterval    string // empty if no trial
	TrialFrequency   int
}

// PaddleSubscription is the subset of a Paddle subscription entity the app
// consumes. PriceID/ProductID reflect the first subscription item only — the
// product sells single-item subscriptions.
type PaddleSubscription struct {
	ID                  string // "sub_..."
	Status              string
	CustomerID          string
	PriceID             string
	ProductID           string
	CurrentPeriodEndsAt *time.Time // nil for paused and canceled subscriptions
	ScheduledCancelAt   *time.Time // nil if no scheduled cancellation
	CanceledAt          *time.Time
}
