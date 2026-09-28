package store

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
)

type Store interface {
	GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error)
	GetWebEntitiesByUserID(ctx context.Context, userId string) ([]*models.WebEntity, error)
	UpdateWebEntity(ctx context.Context, id string, req models.WebEntity) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByID(ctx, id)
}

func (s *store) GetWebEntitiesByUserID(ctx context.Context, userId string) ([]*models.WebEntity, error) {
	return models.FindWebEntitiesByUserID(ctx, userId)
}

func (s *store) UpdateWebEntity(ctx context.Context, id string, req models.WebEntity) error {
	return models.UpdateWebEntity(ctx, id, req)
}
