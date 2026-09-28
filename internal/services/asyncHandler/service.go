package asyncHandler

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type AsyncHandler struct {
	queue           interfaces.Queue
	idempotency     interfaces.IdempotencyStore
	registry        map[pipeline.ProcessType]ProcessHandler
	workerCount     int
	maxRetries      int
	shutdownTimeout time.Duration
	// visibilityOverrides maps a process type to a per-message visibility
	// timeout (seconds). On receipt of such a message the handler extends its
	// visibility before running, so a slow handler isn't redelivered mid-flight.
	// Process types absent from the map keep the queue's default timeout.
	visibilityOverrides map[pipeline.ProcessType]int32
	wg                  sync.WaitGroup
	sem                 chan struct{}
}

// NewAsyncHandler builds the handler. idempotency may be nil, in which case
// duplicate-delivery protection is disabled (e.g. in tests); when set, it is
// consulted before and after each handler run to drop redeliveries.
func NewAsyncHandler(
	queue interfaces.Queue,
	idempotency interfaces.IdempotencyStore,
	registry map[pipeline.ProcessType]ProcessHandler,
	workerCount int,
	maxRetries int,
	shutdownTimeout time.Duration,
	visibilityOverrides map[pipeline.ProcessType]int32,
) *AsyncHandler {
	return &AsyncHandler{
		queue:               queue,
		idempotency:         idempotency,
		registry:            registry,
		workerCount:         workerCount,
		maxRetries:          maxRetries,
		shutdownTimeout:     shutdownTimeout,
		visibilityOverrides: visibilityOverrides,
		sem:                 make(chan struct{}, workerCount),
	}
}

// Start begins consuming from the queue. Blocks until ctx is cancelled.
// After cancellation, waits for in-flight goroutines up to shutdownTimeout.
func (h *AsyncHandler) Start(ctx context.Context) error {
	log.Info("async handler starting", "workerCount", h.workerCount)

	err := h.queue.Consume(ctx, func(msgCtx context.Context, body []byte, receiptHandle string) {
		h.sem <- struct{}{} // acquire semaphore slot
		h.wg.Add(1)

		go func() {
			defer h.wg.Done()
			defer func() { <-h.sem }()

			h.processMessage(msgCtx, body, receiptHandle)
		}()
	})

	log.Info("async handler shutting down, waiting for in-flight processes")
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info("async handler: all in-flight processes completed")
	case <-time.After(h.shutdownTimeout):
		log.Warn("async handler: shutdown timeout exceeded, some processes may not have completed")
	}

	return err
}

func (h *AsyncHandler) processMessage(ctx context.Context, body []byte, receiptHandle string) {
	var msg MessageEnvelope
	if err := json.Unmarshal(body, &msg); err != nil {
		log.Error("failed to deserialize message envelope",
			"error", err,
		)
		if dlqErr := h.queue.SendToDLQ(ctx, body); dlqErr != nil {
			log.Error("failed to send malformed message to DLQ", "error", dlqErr)
		}
		if ackErr := h.queue.Acknowledge(ctx, receiptHandle); ackErr != nil {
			log.Error("failed to acknowledge malformed message", "error", ackErr, "receiptHandle", receiptHandle)
		}
		return
	}

	handler, ok := h.registry[msg.ProcessType]
	if !ok {
		log.Error("no handler registered for process type",
			"processType", msg.ProcessType,
			"messageId", msg.MessageID,
		)
		if dlqErr := h.queue.SendToDLQ(ctx, body); dlqErr != nil {
			log.Error("failed to send unhandled message to DLQ", "error", dlqErr)
		}
		if ackErr := h.queue.Acknowledge(ctx, receiptHandle); ackErr != nil {
			log.Error("failed to acknowledge unhandled message", "error", ackErr, "messageId", msg.MessageID)
		}
		return
	}

	// Idempotency gate: SQS is at-least-once, so a message whose handler
	// already succeeded can be redelivered (e.g. the ack was lost mid-outage).
	// Skip work a prior delivery completed; a store error fails open so a
	// dedupe-backend outage can't stall the queue (worst case is a duplicate,
	// which is the pre-existing behaviour).
	if h.idempotency != nil {
		alreadyProcessed, err := h.idempotency.BeginProcessing(ctx, msg.MessageID)
		if err != nil {
			log.Error("idempotency check failed, processing anyway", "error", err, "messageId", msg.MessageID)
		} else if alreadyProcessed {
			log.Info("duplicate delivery skipped",
				"processType", msg.ProcessType,
				"messageId", msg.MessageID,
			)
			if ackErr := h.queue.Acknowledge(ctx, receiptHandle); ackErr != nil {
				log.Error("failed to acknowledge duplicate message", "error", ackErr, "messageId", msg.MessageID)
			}
			return
		}
	}

	log.Info("processing message",
		"processType", msg.ProcessType,
		"messageId", msg.MessageID,
		"userId", msg.UserID,
		"retryCount", msg.Metadata.RetryCount,
	)

	// Extend visibility for process types that run longer than the queue
	// default (e.g. a long LLM call). A failure here only risks an early redelivery,
	// which the idempotency gate above absorbs, so log and proceed.
	if seconds, ok := h.visibilityOverrides[msg.ProcessType]; ok {
		if err := h.queue.ExtendVisibility(ctx, receiptHandle, seconds); err != nil {
			log.Error("failed to extend message visibility",
				"error", err,
				"processType", msg.ProcessType,
				"messageId", msg.MessageID,
			)
		}
	}

	start := time.Now()
	if err := handler(ctx, msg); err != nil {
		log.Error("process handler failed",
			"error", err,
			"processType", msg.ProcessType,
			"messageId", msg.MessageID,
			"userId", msg.UserID,
			"retryCount", msg.Metadata.RetryCount,
			"duration", time.Since(start),
		)
		h.handleFailure(ctx, msg, receiptHandle, err)
		return
	}

	// Mark BEFORE the ack: if we crash in the gap, the redelivery is skipped by
	// the gate above. A failed mark only risks a future duplicate (handler
	// already succeeded), so log and proceed to ack rather than reprocess.
	if h.idempotency != nil {
		if err := h.idempotency.MarkProcessed(ctx, msg.MessageID); err != nil {
			log.Error("failed to mark message processed", "error", err, "messageId", msg.MessageID)
		}
	}

	if err := h.queue.Acknowledge(ctx, receiptHandle); err != nil {
		log.Error("failed to acknowledge message", "error", err, "messageId", msg.MessageID)
	}

	log.Info("message processed successfully",
		"processType", msg.ProcessType,
		"messageId", msg.MessageID,
		"duration", time.Since(start),
	)
}

func (h *AsyncHandler) handleFailure(ctx context.Context, msg MessageEnvelope, receiptHandle string, handlerErr error) {
	if err := h.queue.Acknowledge(ctx, receiptHandle); err != nil {
		log.Error("failed to acknowledge failed message", "error", err, "messageId", msg.MessageID)
	}

	msg.Metadata.RetryCount++

	// Permanent failures short-circuit: retrying cannot change the outcome
	// (revoked API key, missing required field mapping, …), so route straight
	// to the DLQ instead of re-enqueueing until MaxRetries.
	if errors.Is(handlerErr, pipeline.ErrPermanent) {
		log.Warn("permanent failure, sending to DLQ without retry",
			"processType", msg.ProcessType,
			"messageId", msg.MessageID,
			"error", handlerErr,
			"correlationId", msg.Metadata.CorrelationID,
		)
		retryBody, _ := json.Marshal(msg)
		if err := h.queue.SendToDLQ(ctx, retryBody); err != nil {
			log.Error("failed to send permanent-failure message to DLQ", "error", err, "messageId", msg.MessageID)
		}
		return
	}

	if msg.Metadata.RetryCount >= msg.Metadata.MaxRetries {
		log.Warn("max retries exceeded, sending to DLQ",
			"processType", msg.ProcessType,
			"messageId", msg.MessageID,
			"retryCount", msg.Metadata.RetryCount,
			"correlationId", msg.Metadata.CorrelationID,
		)
		retryBody, _ := json.Marshal(msg)
		if err := h.queue.SendToDLQ(ctx, retryBody); err != nil {
			log.Error("failed to send message to DLQ", "error", err, "messageId", msg.MessageID)
		}
		return
	}

	log.Info("re-enqueuing message for retry",
		"processType", msg.ProcessType,
		"messageId", msg.MessageID,
		"retryCount", msg.Metadata.RetryCount,
	)
	retryBody, _ := json.Marshal(msg)
	if err := h.queue.Enqueue(ctx, retryBody); err != nil {
		log.Error("failed to re-enqueue message", "error", err, "messageId", msg.MessageID)
	}
}
