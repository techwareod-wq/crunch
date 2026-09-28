package store

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the data-access surface of the admin deletion cascade, delegating
// 1:1 to internal/models. Every Delete* is a raw DeleteMany underneath — zero
// matches is success, which is what makes cascade re-runs converge.
type Store interface {
	FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error)
	// TombstoneSubscriptions must be called BEFORE DeleteSubscriptionsByUserID
	// so no webhook can slip between doc delete and guard.
	TombstoneSubscriptions(ctx context.Context, userID primitive.ObjectID, paddleSubIDs []string, by string) error
	DeleteSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)
	// RecomputeUserEntitlement upholds the invariant that every subscriptions
	// write reprojects users.entitlements (zero subs → projection unset).
	RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error

	FindWebEntityContextIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error)
	FindMasterContextImageS3KeysByUserID(ctx context.Context, userID primitive.ObjectID) ([]string, error)
	DeleteWebEntityMasterContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)
	DeleteScheduledArticlesByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)
	DeleteKeywordsForWECs(ctx context.Context, wecIDs []primitive.ObjectID) (int64, error)
	DeleteWebEntityContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)
	FindWebEntityIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error)
	DeleteAnalyticsRawByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error)
	DeleteAnalyticsFactsByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error)
	DeleteWebEntityByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)

	DeletePaddleCustomersByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error)
	DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error) {
	return models.FindSubscriptionsByUserID(ctx, userID)
}

func (s *store) TombstoneSubscriptions(ctx context.Context, userID primitive.ObjectID, paddleSubIDs []string, by string) error {
	return models.TombstoneSubscriptions(ctx, userID, paddleSubIDs, by)
}

func (s *store) DeleteSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeleteSubscriptionsByUserID(ctx, userID)
}

func (s *store) RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error {
	return models.RecomputeUserEntitlement(ctx, userID, appID)
}

func (s *store) FindWebEntityContextIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	return models.FindWebEntityContextIDsByUserID(ctx, userID)
}

func (s *store) FindMasterContextImageS3KeysByUserID(ctx context.Context, userID primitive.ObjectID) ([]string, error) {
	return models.FindMasterContextImageS3KeysByUserID(ctx, userID)
}

func (s *store) DeleteWebEntityMasterContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeleteWebEntityMasterContextsByUserID(ctx, userID)
}

func (s *store) DeleteScheduledArticlesByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeleteScheduledArticlesByUserID(ctx, userID)
}

func (s *store) DeleteKeywordsForWECs(ctx context.Context, wecIDs []primitive.ObjectID) (int64, error) {
	return models.DeleteKeywordsForWECs(ctx, wecIDs)
}

func (s *store) DeleteWebEntityContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeleteWebEntityContextsByUserID(ctx, userID)
}

func (s *store) FindWebEntityIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	return models.FindWebEntityIDsByUserID(ctx, userID)
}

func (s *store) DeleteAnalyticsRawByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	return models.DeleteAnalyticsRawByWebEntityIDs(ctx, entityIDs)
}

func (s *store) DeleteAnalyticsFactsByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	return models.DeleteAnalyticsFactsByWebEntityIDs(ctx, entityIDs)
}

func (s *store) DeleteWebEntityByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeleteWebEntityByUserID(ctx, userID)
}

func (s *store) DeletePaddleCustomersByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.DeletePaddleCustomersByUserID(ctx, userID)
}

func (s *store) DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error {
	return models.DeactivateAndScrubUserByID(ctx, id)
}
