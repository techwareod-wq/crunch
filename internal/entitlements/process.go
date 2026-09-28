package entitlements

import (
	"time"

	"github.com/atharva-ng/crunch/internal/pipeline"
)

// Trial lifecycle process types. Dispatched by the payment webhook via the
// two-marker pattern (fact set-once, dispatch marker best-effort), so
// delivery is AT-LEAST-ONCE — handlers must be idempotent, keyed on
// PaddleSubscriptionID.
const (
	ProcessTrialConverted pipeline.ProcessType = "entitlements_trial_converted"
	ProcessTrialExpired   pipeline.ProcessType = "entitlements_trial_expired"
)

type TrialConvertedPayload struct {
	AppID                string    `json:"appId"`
	PaddleSubscriptionID string    `json:"paddleSubscriptionId"`
	PriceID              string    `json:"priceId"`
	ConvertedAt          time.Time `json:"convertedAt"`
}

type TrialExpiredPayload struct {
	AppID                string    `json:"appId"`
	PaddleSubscriptionID string    `json:"paddleSubscriptionId"`
	PriceID              string    `json:"priceId"`
	ExpiredAt            time.Time `json:"expiredAt"`
}
