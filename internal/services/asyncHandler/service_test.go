package asyncHandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

const testProcess pipeline.ProcessType = "TEST_PROCESS"

type fakeQueue struct {
	enqueued [][]byte
	dlq      [][]byte
	acks     []string
}

func (q *fakeQueue) Consume(context.Context, interfaces.MessageCallback) error { return nil }
func (q *fakeQueue) Enqueue(_ context.Context, body []byte) error {
	q.enqueued = append(q.enqueued, body)
	return nil
}
func (q *fakeQueue) Acknowledge(_ context.Context, receiptHandle string) error {
	q.acks = append(q.acks, receiptHandle)
	return nil
}
func (q *fakeQueue) ExtendVisibility(context.Context, string, int32) error { return nil }
func (q *fakeQueue) SendToDLQ(_ context.Context, body []byte) error {
	q.dlq = append(q.dlq, body)
	return nil
}

func newTestHandler(q interfaces.Queue, handler ProcessHandler) *AsyncHandler {
	return NewAsyncHandler(
		q,
		nil, // idempotency disabled
		map[pipeline.ProcessType]ProcessHandler{testProcess: handler},
		1,
		3, // MaxRetries
		0,
		nil,
	)
}

func envelopeBody(t *testing.T, retryCount int) []byte {
	t.Helper()
	body, err := json.Marshal(MessageEnvelope{
		MessageID:   "msg-1",
		ProcessType: testProcess,
		UserID:      "user-1",
		Payload:     json.RawMessage(`{}`),
		Metadata:    MessageMetadata{RetryCount: retryCount, MaxRetries: 3},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

func TestTransientFailureReenqueues(t *testing.T) {
	q := &fakeQueue{}
	h := newTestHandler(q, func(context.Context, MessageEnvelope) error {
		return errors.New("transient blip")
	})

	h.processMessage(context.Background(), envelopeBody(t, 0), "rh-1")

	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 re-enqueue, got %d (dlq %d)", len(q.enqueued), len(q.dlq))
	}
	if len(q.dlq) != 0 {
		t.Fatalf("transient failure must not DLQ on first attempt, got %d", len(q.dlq))
	}

	var retried MessageEnvelope
	if err := json.Unmarshal(q.enqueued[0], &retried); err != nil {
		t.Fatalf("unmarshal retried message: %v", err)
	}
	if retried.Metadata.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", retried.Metadata.RetryCount)
	}
}

func TestPermanentFailureShortCircuitsToDLQ(t *testing.T) {
	q := &fakeQueue{}
	h := newTestHandler(q, func(context.Context, MessageEnvelope) error {
		return fmt.Errorf("%w: framer rejected the api key", pipeline.ErrPermanent)
	})

	h.processMessage(context.Background(), envelopeBody(t, 0), "rh-1")

	if len(q.dlq) != 1 {
		t.Fatalf("expected permanent failure to DLQ immediately, got dlq=%d enqueued=%d", len(q.dlq), len(q.enqueued))
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("permanent failure must not re-enqueue, got %d", len(q.enqueued))
	}
	if len(q.acks) != 1 {
		t.Fatalf("failed message must still be acknowledged, got %d acks", len(q.acks))
	}
}

func TestWrappedPermanentSentinelIsDetected(t *testing.T) {
	q := &fakeQueue{}
	inner := fmt.Errorf("content bridge: %w", fmt.Errorf("publish: %w", pipeline.ErrPermanent))
	h := newTestHandler(q, func(context.Context, MessageEnvelope) error { return inner })

	h.processMessage(context.Background(), envelopeBody(t, 0), "rh-1")

	if len(q.dlq) != 1 || len(q.enqueued) != 0 {
		t.Fatalf("deeply wrapped ErrPermanent must DLQ: dlq=%d enqueued=%d", len(q.dlq), len(q.enqueued))
	}
}

func TestMaxRetriesStillDLQs(t *testing.T) {
	q := &fakeQueue{}
	h := newTestHandler(q, func(context.Context, MessageEnvelope) error {
		return errors.New("still transient")
	})

	// RetryCount 2 + this failure = 3 = MaxRetries → DLQ.
	h.processMessage(context.Background(), envelopeBody(t, 2), "rh-1")

	if len(q.dlq) != 1 || len(q.enqueued) != 0 {
		t.Fatalf("max retries must DLQ: dlq=%d enqueued=%d", len(q.dlq), len(q.enqueued))
	}
}
