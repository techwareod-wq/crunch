package store

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Store interface {
	GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error)
	GetScheduledArticlesByWebEntityInRange(ctx context.Context, userID, webEntityID string, from, to time.Time) ([]*models.ScheduledArticle, error)
	GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error)
	GetWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error)
	GetMasterContextsByScheduledArticleIDs(ctx context.Context, scheduledArticleIDs []string) ([]models.WebEntityMasterContext, error)
	GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error)
	GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error)
	UpdateWebEntityMasterContext(ctx context.Context, id string, req models.WEMCUpdateReq) error
	SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error
	SetScheduledArticlePublishAsLive(ctx context.Context, id string, live bool) error
	UpdateScheduledArticle(ctx context.Context, id string, req models.ScheduledArticleUpdateReq) error
	DeleteScheduledArticle(ctx context.Context, id string) error
	DeleteMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) error

	// Pending-regen lifecycle: discard endpoints remove single entries, a
	// draft save clears the whole set (the regens are committed by then).
	RemovePendingSectionRegen(ctx context.Context, masterContextID string, sectionID int64) error
	RemovePendingImageRegen(ctx context.Context, masterContextID, position string) (*models.CGEPendingImageRegen, error)
	ClearPendingRegen(ctx context.Context, masterContextID string) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByID(ctx, id)
}

func (s *store) GetScheduledArticlesByWebEntityInRange(ctx context.Context, userID, webEntityID string, from, to time.Time) ([]*models.ScheduledArticle, error) {
	return models.GetScheduledArticlesByWebEntityInRange(ctx, userID, webEntityID, from, to)
}

func (s *store) GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
	return models.GetScheduledArticle(ctx, id)
}

func (s *store) GetWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error) {
	return models.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
}

func (s *store) RemovePendingSectionRegen(ctx context.Context, masterContextID string, sectionID int64) error {
	return models.RemovePendingSectionRegen(ctx, masterContextID, sectionID)
}

func (s *store) RemovePendingImageRegen(ctx context.Context, masterContextID, position string) (*models.CGEPendingImageRegen, error) {
	return models.RemovePendingImageRegen(ctx, masterContextID, position)
}

func (s *store) ClearPendingRegen(ctx context.Context, masterContextID string) error {
	return models.ClearPendingRegen(ctx, masterContextID)
}

func (s *store) GetMasterContextsByScheduledArticleIDs(ctx context.Context, scheduledArticleIDs []string) ([]models.WebEntityMasterContext, error) {
	return models.GetMasterContextsByScheduledArticleIDs(ctx, scheduledArticleIDs)
}

func (s *store) GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error) {
	return models.GetKeyword(ctx, id)
}

func (s *store) GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error) {
	return models.GetKeywordsByIDs(ctx, ids)
}

func (s *store) UpdateWebEntityMasterContext(ctx context.Context, id string, req models.WEMCUpdateReq) error {
	return models.UpdateWebEntityMasterContext(ctx, id, req)
}

func (s *store) SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error {
	return models.SetScheduledArticleStatus(ctx, id, status)
}

func (s *store) SetScheduledArticlePublishAsLive(ctx context.Context, id string, live bool) error {
	return models.SetScheduledArticlePublishAsLive(ctx, id, live)
}

func (s *store) UpdateScheduledArticle(ctx context.Context, id string, req models.ScheduledArticleUpdateReq) error {
	return models.UpdateScheduledArticle(ctx, id, req)
}

func (s *store) DeleteScheduledArticle(ctx context.Context, id string) error {
	return models.DeleteScheduledArticle(ctx, id)
}

func (s *store) DeleteMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) error {
	return models.DeleteWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
}
