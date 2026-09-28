package store

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Store is the persistence boundary for the Scheduling Engine. It hides the
// raw model package from handlers and lets us swap implementations in tests.
type Store interface {
	GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error)
	GetWebEntityContext(ctx context.Context, id string) (bool, *models.WebEntityContext, error)
	GetWebEntityContextForUser(ctx context.Context, webEntityID, userID string) (bool, *models.WebEntityContext, error)
	// TryAdvanceWECStatus is a CAS status transition: it flips status to
	// toStatus only when the current status is one of fromStatuses, returning
	// whether this caller won the transition. Backs the single-shot scheduling
	// completion side effect (see markSchedulingDoneIfComplete).
	TryAdvanceWECStatus(ctx context.Context, id string, fromStatuses []int, toStatus int) (bool, error)
	SetSchedulingTotal(ctx context.Context, id string, total int) error

	GetKeywordsForWEC(ctx context.Context, webEntityContextID string) ([]models.Keyword, error)
	GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error)
	GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error)

	CreateScheduledArticle(ctx context.Context, article *models.ScheduledArticle) error
	CreateScheduledArticles(ctx context.Context, articles []*models.ScheduledArticle) error
	GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error)
	UpdateScheduledArticle(ctx context.Context, id string, req models.ScheduledArticleUpdateReq) error
	CountScheduledArticlesForContext(ctx context.Context, webEntityContextID string) (int64, error)
	CountScheduledArticlesWithTitleForContext(ctx context.Context, webEntityContextID string) (int64, error)
	GetFirstScheduledArticleForContext(ctx context.Context, webEntityContextID string) (bool, *models.ScheduledArticle, error)

	// Upgrade schedule-extension surface (SE_EXTEND_SCHEDULE).
	GetScheduledArticlesByWebEntityContext(ctx context.Context, webEntityContextID string) ([]*models.ScheduledArticle, error)
	SetWebEntityPublishingCadence(ctx context.Context, webEntityID string, articlesPerWeek int) error
	FinalizeWECUpgrade(ctx context.Context, webEntityContextID string) error

	// Rerun surface (SE_RERUN_SCHEDULE).
	SetSchedulingRerunMarker(ctx context.Context, webEntityContextID, rerunKey string, anchor time.Time) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByID(ctx, id)
}

func (s *store) GetWebEntityContext(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
	return models.GetWebEntityContext(ctx, id)
}

func (s *store) GetWebEntityContextForUser(ctx context.Context, webEntityID, userID string) (bool, *models.WebEntityContext, error) {
	return models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityID, userID)
}

func (s *store) TryAdvanceWECStatus(ctx context.Context, id string, fromStatuses []int, toStatus int) (bool, error) {
	return models.TryAdvanceStatus(ctx, id, fromStatuses, toStatus)
}

func (s *store) SetSchedulingTotal(ctx context.Context, id string, total int) error {
	return models.SetSchedulingTotal(ctx, id, total)
}

func (s *store) CountScheduledArticlesWithTitleForContext(ctx context.Context, webEntityContextID string) (int64, error) {
	return models.CountScheduledArticlesWithTitleForContext(ctx, webEntityContextID)
}

func (s *store) CreateScheduledArticle(ctx context.Context, article *models.ScheduledArticle) error {
	return models.CreateScheduledArticle(ctx, article)
}

func (s *store) CreateScheduledArticles(ctx context.Context, articles []*models.ScheduledArticle) error {
	return models.CreateScheduledArticles(ctx, articles)
}

func (s *store) GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
	return models.GetScheduledArticle(ctx, id)
}

func (s *store) UpdateScheduledArticle(ctx context.Context, id string, req models.ScheduledArticleUpdateReq) error {
	return models.UpdateScheduledArticle(ctx, id, req)
}

func (s *store) CountScheduledArticlesForContext(ctx context.Context, webEntityContextID string) (int64, error) {
	return models.CountScheduledArticlesForContext(ctx, webEntityContextID)
}

func (s *store) GetFirstScheduledArticleForContext(ctx context.Context, webEntityContextID string) (bool, *models.ScheduledArticle, error) {
	return models.GetFirstScheduledArticleForContext(ctx, webEntityContextID)
}

func (s *store) GetScheduledArticlesByWebEntityContext(ctx context.Context, webEntityContextID string) ([]*models.ScheduledArticle, error) {
	return models.GetScheduledArticlesByWebEntityContext(ctx, webEntityContextID)
}

func (s *store) SetWebEntityPublishingCadence(ctx context.Context, webEntityID string, articlesPerWeek int) error {
	return models.PartialUpdateWebEntity(ctx, webEntityID, bson.M{"publishing.articles_per_week": articlesPerWeek}, nil)
}

func (s *store) FinalizeWECUpgrade(ctx context.Context, webEntityContextID string) error {
	return models.FinalizeWECUpgrade(ctx, webEntityContextID)
}

func (s *store) SetSchedulingRerunMarker(ctx context.Context, webEntityContextID, rerunKey string, anchor time.Time) error {
	return models.SetSchedulingRerunMarker(ctx, webEntityContextID, rerunKey, anchor)
}

func (s *store) GetKeywordsForWEC(ctx context.Context, webEntityContextID string) ([]models.Keyword, error) {
	return models.GetKeywordsForWEC(ctx, webEntityContextID)
}

func (s *store) GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error) {
	return models.GetKeyword(ctx, id)
}

func (s *store) GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error) {
	return models.GetKeywordsByIDs(ctx, ids)
}
