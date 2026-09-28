package interfaces

import "context"

// MessageCallback is invoked once per received message.
type MessageCallback func(ctx context.Context, body []byte, receiptHandle string)

// Queue defines the contract for async message queue operations.
type Queue interface {
	// Consume starts receiving messages and invokes the callback for each one.
	// Blocks until ctx is cancelled.
	Consume(ctx context.Context, callback MessageCallback) error

	// Enqueue publishes a message to the primary queue.
	Enqueue(ctx context.Context, body []byte) error

	// Acknowledge removes a message from the queue after successful processing.
	Acknowledge(ctx context.Context, receiptHandle string) error

	// ExtendVisibility resets the visibility timeout of an in-flight message to
	// seconds from now, buying a slow handler more time before redelivery.
	ExtendVisibility(ctx context.Context, receiptHandle string, seconds int32) error

	// SendToDLQ routes a message to the dead-letter queue.
	SendToDLQ(ctx context.Context, body []byte) error
}
