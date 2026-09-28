package store

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

type Store interface {
	GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error)
	FindWebEntitiesByUserID(ctx context.Context, userId string) ([]*models.WebEntity, error)
	CreateWebEntity(ctx context.Context, entity *models.WebEntity) error
	UpdateWebEntity(ctx context.Context, id string, req models.WebEntity) error
	PartialUpdateWebEntity(ctx context.Context, id string, set bson.M, unset bson.M) error
	GetWebEntityContextByWebEntityAndUserID(ctx context.Context, webEntityId, userId string) (bool, *models.WebEntityContext, error)
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByID(ctx, id)
}

func (s *store) FindWebEntitiesByUserID(ctx context.Context, userId string) ([]*models.WebEntity, error) {
	return models.FindWebEntitiesByUserID(ctx, userId)
}

func (s *store) CreateWebEntity(ctx context.Context, entity *models.WebEntity) error {
	return models.CreateWebEntity(ctx, entity)
}

func (s *store) UpdateWebEntity(ctx context.Context, id string, req models.WebEntity) error {
	return models.UpdateWebEntity(ctx, id, req)
}

func (s *store) PartialUpdateWebEntity(ctx context.Context, id string, set bson.M, unset bson.M) error {
	return models.PartialUpdateWebEntity(ctx, id, set, unset)
}

func (s *store) GetWebEntityContextByWebEntityAndUserID(ctx context.Context, webEntityId, userId string) (bool, *models.WebEntityContext, error) {
	return models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityId, userId)
}
