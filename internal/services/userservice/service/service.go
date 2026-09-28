package service

import (
	"context"

	errors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/userservice"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
	userstore "github.com/atharva-ng/crunch/internal/services/userservice/store"
	"github.com/atharva-ng/crunch/internal/util/log"
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
		// A JWT-stub user may have been minted before their email was known —
		// the personal-company hook re-runs here (idempotent via the D20
		// index) so every emailed user ends up with one.
		svc.ensurePersonalCompany(ctx, existing.ID, req.Email, req.Name)
		return nil
	}

	user := &models.User{
		ClerkID: req.ClerkID,
		Email:   req.Email,
		Name:    req.Name,
		Role:    models.RoleKeyUser,
	}
	if err := svc.store.CreateUser(ctx, user); err != nil {
		return err
	}
	svc.ensurePersonalCompany(ctx, user.ID, user.Email, user.Name)
	return nil
}

// ensurePersonalCompany is the signup hook (tenancy plan §3.1/D20): every
// user gets a personal company + claimed owner membership. Best-effort — a
// failure must not fail the webhook (Clerk would retry the whole sync), and
// the active-company middleware self-heals on the user's next request.
func (svc *service) ensurePersonalCompany(ctx context.Context, userID primitive.ObjectID, email, name string) {
	if email == "" || userID.IsZero() {
		return
	}
	if _, err := svc.store.EnsurePersonalCompany(ctx, userID, email, name); err != nil {
		log.Warn("personal company mint failed on user sync", "user_id", userID.Hex(), "error", err)
	}
}

// DeleteUser soft-deletes a user by stamping deactivated_at. The row is kept
// so subsequent JWTs / webhook retries can be rejected as tombstoned rather
// than silently re-provisioning the account.
//
// The user ID is returned (NilObjectID if we never had a record for this
// clerk_id) so the caller can cancel billing. An already-tombstoned user still
// returns their ID rather than skipping: the webhook retry that lands here is
// usually one whose Paddle cancellation failed the first time round.
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
	// recovery is a rolesmigrate re-seed.
	if user.Role == models.RoleKeySuperuser {
		log.Warn("superuser account deactivated via Clerk webhook — if this was the last superuser, recover with rolesmigrate -seed-admins",
			"clerk_id", clerkID, "user_id", user.ID.Hex())
	}

	if err := svc.store.DeactivateUserByClerkID(ctx, clerkID); err != nil {
		return primitive.NilObjectID, err
	}
	return user.ID, nil
}
