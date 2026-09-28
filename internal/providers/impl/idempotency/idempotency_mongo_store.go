package idempotency

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type mongoStore struct{}

func NewMongoStore(ctx context.Context, ttl time.Duration) (interfaces.IdempotencyStore, error) {
	if err := models.EnsureProcessedMessageIndexes(ctx, ttl); err != nil {
		return nil, err
	}
	return &mongoStore{}, nil
}

func (s *mongoStore) BeginProcessing(ctx context.Context, msgID string) (bool, error) {
	duplicate, existing, err := models.InsertProcessedMessage(ctx, &models.ProcessedMessage{MessageID: msgID})
	if err != nil {
		return false, err
	}
	return duplicate && existing != nil && existing.ProcessedAt != nil, nil
}

func (s *mongoStore) MarkProcessed(ctx context.Context, msgID string) error {
	return models.MarkProcessedMessage(ctx, msgID)
}

var _ interfaces.IdempotencyStore = (*mongoStore)(nil)
