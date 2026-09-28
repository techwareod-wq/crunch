package store

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the thin persistence seam for contentBridge — pass-throughs to
// internal/models, matching the store pattern used across the other services.
type Store interface {
	FindWebEntityByUserID(ctx context.Context, userID string) (bool, *models.WebEntity, error)
	GetMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error)
	GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error)
	GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error)
	SetPublishState(ctx context.Context, masterContextID string, state models.CGEPublishState) error
	SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error
}

type store struct{}

func NewStore() Store { return &store{} }

func (s *store) FindWebEntityByUserID(ctx context.Context, userID string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByUserID(ctx, userID)
}

func (s *store) GetMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error) {
	return models.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
}

func (s *store) GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
	return models.GetScheduledArticle(ctx, id)
}

func (s *store) GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error) {
	return models.GetKeyword(ctx, id)
}

func (s *store) SetPublishState(ctx context.Context, masterContextID string, state models.CGEPublishState) error {
	return models.SetCGEPublishState(ctx, masterContextID, state)
}

func (s *store) SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error {
	return models.SetScheduledArticleStatus(ctx, id, status)
}
