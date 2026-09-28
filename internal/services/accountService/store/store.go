package store

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the data access the deletion cascade needs beyond the per-feature
// DataCleaners.
type Store interface {
	DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error {
	return models.DeactivateAndScrubUserByID(ctx, id)
}
