package store

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the AI search service's data access: listings to embed.
type Store interface {
	GetWarehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error)
	// SetEmbedding stores the vector when the live version still matches;
	// false = skipped (a newer version has its own job).
	SetEmbedding(ctx context.Context, id primitive.ObjectID, liveVersion int, vec []float32, hash string) (bool, error)
	// LiveRefs pages live listings by _id after after.
	LiveRefs(ctx context.Context, after primitive.ObjectID, limit int) ([]models.LiveWarehouseRef, error)
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) GetWarehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	return models.FindWarehouseByID(ctx, id)
}

func (s *store) SetEmbedding(ctx context.Context, id primitive.ObjectID, liveVersion int, vec []float32, hash string) (bool, error) {
	return models.SetWarehouseEmbedding(ctx, id, liveVersion, vec, hash)
}

func (s *store) LiveRefs(ctx context.Context, after primitive.ObjectID, limit int) ([]models.LiveWarehouseRef, error) {
	return models.ListLiveWarehouseRefs(ctx, after, limit)
}
