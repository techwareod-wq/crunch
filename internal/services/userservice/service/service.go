package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	errors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/userservice"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
	userstore "github.com/atharva-ng/crunch/internal/services/userservice/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BSON field keys used by SyncUser's update document.
const (
	bsonFieldEmail = "email"
	bsonFieldName  = "name"
)

type service struct {
	store userstore.Store
}

var _ userservice.UserService = (*service)(nil)

func NewService(s userstore.Store) userservice.UserService {
	return &service{
		store: s,
	}
}

func (svc *service) GetProfile(ctx context.Context, userID string) (*dto.UserResponse, error) {
	found, user, err := svc.store.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.ErrUserNotFound
	}

	result := &dto.UserResponse{}
	if err := result.FromDbModel(user); err != nil {
		return nil, err
	}
	return result, nil
}

// maxCompanyLen bounds the free-text company name.
const maxCompanyLen = 200

// UpdateProfile normalises the phone to E.164 (default region IN, D-100) and
// stores phone + company. An empty phone clears both phone fields.
func (svc *service) UpdateProfile(ctx context.Context, userID string, req dto.UpdateProfileRequest) (*dto.UserResponse, error) {
	phone := strings.TrimSpace(req.Phone)
	company := strings.TrimSpace(req.Company)
	if utf8.RuneCountInString(company) > maxCompanyLen {
		return nil, errors.ErrInvalidRequestBody
	}
	e164 := ""
	if phone != "" {
		var err error
		if e164, err = utils.NormalizePhone(phone, utils.DefaultPhoneRegion); err != nil {
			return nil, errors.ErrInvalidPhone
		}
	}
	if err := svc.store.UpdateUserByID(ctx, userID, bson.M{
		"phone":              phone,
		"phone_e164":         e164,
		"company":            company,
		"profile_updated_at": time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return svc.GetProfile(ctx, userID)
}

// SyncUser creates or updates a local user record from Clerk webhook data.
//
// Tombstone-aware: if a deactivated user already exists for this clerk_id, we
// refuse to resurrect them. This protects against out-of-order webhook delivery
// (a delayed user.created/user.updated arriving after user.deleted) silently
// undoing the soft delete.
func (svc *service) SyncUser(ctx context.Context, req dto.SyncUserRequest) error {
	found, existing, err := svc.store.FindUserByClerkIDIncludingDeactivated(ctx, req.ClerkID)
	if err != nil {
		return err
	}

	if found && existing.DeactivatedAt != nil {
		log.Warn("user sync skipped: clerk_id is tombstoned", "clerk_id", req.ClerkID)
		return nil
	}

	if found {
		if err := svc.store.UpdateUserByClerkID(ctx, req.ClerkID, bson.M{
			bsonFieldEmail: req.Email,
			bsonFieldName:  req.Name,
		}); err != nil {
			return err
		}
		return nil
	}

	user := &models.User{
		ClerkID: req.ClerkID,
		Email:   req.Email,
		Name:    req.Name,
		Role:    models.RoleUser,
	}
	return svc.store.CreateUser(ctx, user)
}

// DeleteUser soft-deletes a user by stamping deactivated_at. The row is kept
// so subsequent JWTs / webhook retries can be rejected as tombstoned rather
// than silently re-provisioning the account.
//
// The user ID is returned (NilObjectID if we never had a record for this
// clerk_id) so the caller can tear down what hangs off the account. An
// already-tombstoned user still returns their ID rather than skipping, so a
// webhook retry can finish a teardown that failed the first time round.
func (svc *service) DeleteUser(ctx context.Context, clerkID string) (primitive.ObjectID, error) {
	found, user, err := svc.store.FindUserByClerkIDIncludingDeactivated(ctx, clerkID)
	if err != nil {
		return primitive.NilObjectID, err
	}
	if !found {
		log.Warn("user delete skipped: no local record for clerk_id", "clerk_id", clerkID)
		return primitive.NilObjectID, nil
	}

	// Accepted-risk log (RBAC plan §7): the last superuser deleting their own
	// Clerk account tombstones them outside the admin gate, so the last-holder
	// guard never fires. Log loudly so a resulting lockout is diagnosable —
	// recovery is cmd/superuser.
	if user.Role == models.RoleSuperuser {
		log.Warn("superuser account deactivated via Clerk webhook — if this was the last superuser, recover with cmd/superuser",
			"clerk_id", clerkID, "user_id", user.ID.Hex())
	}

	if err := svc.store.DeactivateUserByClerkID(ctx, clerkID); err != nil {
		return primitive.NilObjectID, err
	}
	return user.ID, nil
}
