package sqs

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	appconfig "github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// gatedSQSProvider wraps a sqsProvider and checks a gate function before
// each poll. When the gate returns false (e.g. token budget exhausted),
// consumption pauses for sleepDuration before re-checking.
type gatedSQSProvider struct {
	*sqsProvider
	gate          func() bool
	sleepDuration time.Duration
}

// NewGatedSQS creates a Queue whose Consume loop respects a gate function.
// Enqueue, Acknowledge, and SendToDLQ are inherited from the underlying provider.
func NewGatedSQS(awsCfg appconfig.AWSConfig, sqsCfg appconfig.SQSConfig, gate func() bool, sleepDuration time.Duration) (interfaces.Queue, error) {
	inner, err := newSQSProvider(awsCfg, sqsCfg)
	if err != nil {
		return nil, err
	}
	return &gatedSQSProvider{
		sqsProvider:   inner,
		gate:          gate,
		sleepDuration: sleepDuration,
	}, nil
}

func (g *gatedSQSProvider) Consume(ctx context.Context, callback interfaces.MessageCallback) error {
	for {
		select {
		case <-ctx.Done():
			log.Info("gated SQS consumer stopping")
			return ctx.Err()
		default:
		}

		if !g.gate() {
			log.Info("token limit exceeded, pausing secondary queue consumption")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(g.sleepDuration):
			}
			continue
		}

		output, err := g.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            &g.queueURL,
			MaxNumberOfMessages: g.maxMessagesPerBatch,
			WaitTimeSeconds:     g.waitTimeSeconds,
			VisibilityTimeout:   g.visibilityTimeout,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error("gated SQS receive error", "error", err)
			continue
		}

		for _, sqsMsg := range output.Messages {
			callback(ctx, []byte(*sqsMsg.Body), *sqsMsg.ReceiptHandle)
		}
	}
}

var _ interfaces.Queue = (*gatedSQSProvider)(nil)
