package userservice

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
)

type UserService interface {
	GetProfile(ctx context.Context, userID string) (*dto.UserResponse, error)
	// UpdateProfile validates and stores the visitor's phone + company
	// (D-100) and returns the updated profile.
	UpdateProfile(ctx context.Context, userID string, req dto.UpdateProfileRequest) (*dto.UserResponse, error)
	SyncUser(ctx context.Context, req dto.SyncUserRequest) error
	// DeleteUser soft-deletes the user and returns their ID so the caller can
	// tear down what hangs off the account. Returns
	// NilObjectID when no local user exists for this clerk_id.
	DeleteUser(ctx context.Context, clerkID string) (primitive.ObjectID, error)
}
