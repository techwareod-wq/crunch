package asyncHandler

import (
	"encoding/json"
	"time"

	"github.com/atharva-ng/crunch/internal/pipeline"
)

type MessageEnvelope struct {
	MessageID   string               `json:"messageId"`
	ProcessType pipeline.ProcessType `json:"processType"`
	UserID      string               `json:"userId"`
	Payload     json.RawMessage      `json:"payload"`
	Metadata    MessageMetadata      `json:"metadata"`
}

type MessageMetadata struct {
	RetryCount    int       `json:"retryCount"`
	MaxRetries    int       `json:"maxRetries"`
	Source        string    `json:"source"`
	CorrelationID string    `json:"correlationId"`
	CreatedAt     time.Time `json:"createdAt"`
}
