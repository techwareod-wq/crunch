package sqs

import (
	"context"
	"fmt"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	appconfig "github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type sqsProvider struct {
	client              *sqs.Client
	queueURL            string
	dlqURL              string
	waitTimeSeconds     int32
	visibilityTimeout   int32
	maxMessagesPerBatch int32
}

func newSQSProvider(awsCfg appconfig.AWSConfig, sqsCfg appconfig.SQSConfig) (*sqsProvider, error) {
	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(awsCfg.Region),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			awsCfg.AccessKeyID, awsCfg.SecretAccessKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	client := sqs.NewFromConfig(cfg)
	return &sqsProvider{
		client:              client,
		queueURL:            sqsCfg.QueueURL,
		dlqURL:              sqsCfg.DLQUrl,
		waitTimeSeconds:     int32(sqsCfg.WaitTimeSeconds),
		visibilityTimeout:   int32(sqsCfg.VisibilityTimeout),
		maxMessagesPerBatch: int32(sqsCfg.MaxMessagesPerBatch),
	}, nil
}

func NewSQS(awsCfg appconfig.AWSConfig, sqsCfg appconfig.SQSConfig) (interfaces.Queue, error) {
	return newSQSProvider(awsCfg, sqsCfg)
}

func (p *sqsProvider) Consume(ctx context.Context, callback interfaces.MessageCallback) error {
	for {
		select {
		case <-ctx.Done():
			log.Info("SQS consumer stopping")
			return ctx.Err()
		default:
		}

		output, err := p.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            &p.queueURL,
			MaxNumberOfMessages: p.maxMessagesPerBatch,
			WaitTimeSeconds:     p.waitTimeSeconds,
			VisibilityTimeout:   p.visibilityTimeout,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error("SQS receive error", "error", err)
			continue
		}

		for _, sqsMsg := range output.Messages {
			callback(ctx, []byte(*sqsMsg.Body), *sqsMsg.ReceiptHandle)
		}
	}
}

func (p *sqsProvider) Enqueue(ctx context.Context, body []byte) error {
	bodyStr := string(body)
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &p.queueURL,
		MessageBody: &bodyStr,
	})
	return err
}

func (p *sqsProvider) Acknowledge(ctx context.Context, receiptHandle string) error {
	_, err := p.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &p.queueURL,
		ReceiptHandle: &receiptHandle,
	})
	return err
}

func (p *sqsProvider) ExtendVisibility(ctx context.Context, receiptHandle string, seconds int32) error {
	_, err := p.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          &p.queueURL,
		ReceiptHandle:     &receiptHandle,
		VisibilityTimeout: seconds,
	})
	return err
}

func (p *sqsProvider) SendToDLQ(ctx context.Context, body []byte) error {
	bodyStr := string(body)
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &p.dlqURL,
		MessageBody: &bodyStr,
	})
	return err
}

var _ interfaces.Queue = (*sqsProvider)(nil)
