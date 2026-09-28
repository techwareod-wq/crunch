package store

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// Store defines the data-access methods for users.
//
// All Find* methods filter out deactivated users by default. Use
// FindUserByClerkIDIncludingDeactivated when the caller must reason about
// tombstones (e.g. to refuse a resurrect on a stale Clerk webhook or JWT).
type Store interface {
	FindUserByID(ctx context.Context, id string) (bool, *models.User, error)
	FindUserByClerkID(ctx context.Context, clerkID string) (bool, *models.User, error)
	FindUserByClerkIDIncludingDeactivated(ctx context.Context, clerkID string) (bool, *models.User, error)
	FindUserByEmail(ctx context.Context, email string) (bool, *models.User, error)
	CreateUser(ctx context.Context, user *models.User) error
	UpdateUserByClerkID(ctx context.Context, clerkID string, update bson.M) error
	DeactivateUserByClerkID(ctx context.Context, clerkID string) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) FindUserByID(ctx context.Context, id string) (bool, *models.User, error) {
	return models.FindUserByID(ctx, id)
}

func (s *store) FindUserByClerkID(ctx context.Context, clerkID string) (bool, *models.User, error) {
	return models.FindUserByClerkID(ctx, clerkID)
}

func (s *store) FindUserByClerkIDIncludingDeactivated(ctx context.Context, clerkID string) (bool, *models.User, error) {
	return models.FindUserByClerkIDIncludingDeactivated(ctx, clerkID)
}

func (s *store) FindUserByEmail(ctx context.Context, email string) (bool, *models.User, error) {
	return models.FindUserByEmail(ctx, email)
}

func (s *store) CreateUser(ctx context.Context, user *models.User) error {
	return models.CreateUser(ctx, user)
}

func (s *store) UpdateUserByClerkID(ctx context.Context, clerkID string, update bson.M) error {
	return models.UpdateUserByClerkID(ctx, clerkID, update)
}

func (s *store) DeactivateUserByClerkID(ctx context.Context, clerkID string) error {
	return models.DeactivateUserByClerkID(ctx, clerkID)
}
