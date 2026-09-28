package asyncHandler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type dispatcher struct {
	primaryQueue    interfaces.Queue
	secondaryQueue  interfaces.Queue
	llmProcessTypes map[pipeline.ProcessType]bool
	maxRetries      int
}

// NewDispatcher creates a dispatcher that routes LLM process types to the
// secondary queue and everything else to the primary queue.
func NewDispatcher(primaryQueue, secondaryQueue interfaces.Queue, llmProcessTypes []pipeline.ProcessType, maxRetries int) interfaces.Dispatcher {
	llmSet := make(map[pipeline.ProcessType]bool, len(llmProcessTypes))
	for _, pt := range llmProcessTypes {
		llmSet[pt] = true
	}
	return &dispatcher{
		primaryQueue:    primaryQueue,
		secondaryQueue:  secondaryQueue,
		llmProcessTypes: llmSet,
		maxRetries:      maxRetries,
	}
}

func (d *dispatcher) Dispatch(ctx context.Context, processType string, userID string, payload any) error {
	return d.dispatch(ctx, processType, userID, newUUID(), payload)
}

// DispatchKeyed publishes with a caller-chosen MessageID so the consumer-side
// idempotency gate (which dedupes on MessageID) collapses independent
// dispatches of the same logical work into one handler run.
func (d *dispatcher) DispatchKeyed(ctx context.Context, processType string, userID string, messageID string, payload any) error {
	if messageID == "" {
		messageID = newUUID()
	}
	return d.dispatch(ctx, processType, userID, messageID, payload)
}

func (d *dispatcher) dispatch(ctx context.Context, processType string, userID string, messageID string, payload any) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal dispatch payload: %w", err)
	}

	msg := MessageEnvelope{
		MessageID:   messageID,
		ProcessType: pipeline.ProcessType(processType),
		UserID:      userID,
		Payload:     payloadBytes,
		Metadata: MessageMetadata{
			RetryCount:    0,
			MaxRetries:    d.maxRetries,
			Source:        "dispatcher",
			CorrelationID: newUUID(),
			CreatedAt:     time.Now().UTC(),
		},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message envelope: %w", err)
	}

	q := d.primaryQueue
	if d.llmProcessTypes[msg.ProcessType] {
		q = d.secondaryQueue
	}
	return q.Enqueue(ctx, body)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var _ interfaces.Dispatcher = (*dispatcher)(nil)
