// Package staffinvites applies a pending staff invite's role when the invitee
// first signs in (D-011). Two paths reach it — the Clerk user.created webhook
// and JWT auto-create — whichever comes first; both must be idempotent, so
// Apply is safe to run any number of times for the same user.
package staffinvites

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Store is the data access Apply needs (models in production, a fake in
// tests).
type Store interface {
	FindPendingByEmail(ctx context.Context, email string) (bool, *models.StaffInvite, error)
	MarkAccepted(ctx context.Context, id primitive.ObjectID, userID string) error
	MarkClosed(ctx context.Context, id primitive.ObjectID, status string) error
	FindUserByID(ctx context.Context, id string) (bool, *models.User, error)
	// SetUserRole is the same CAS write POST /v1/admin/users/role uses.
	SetUserRole(ctx context.Context, userID primitive.ObjectID, role string, expected *time.Time) error
}

type mongoStore struct{}

func (mongoStore) FindPendingByEmail(ctx context.Context, email string) (bool, *models.StaffInvite, error) {
	return models.FindPendingStaffInviteByEmail(ctx, email)
}
func (mongoStore) MarkAccepted(ctx context.Context, id primitive.ObjectID, userID string) error {
	return models.MarkStaffInviteAccepted(ctx, id, userID)
}
func (mongoStore) MarkClosed(ctx context.Context, id primitive.ObjectID, status string) error {
	return models.MarkStaffInviteClosed(ctx, id, status)
}
func (mongoStore) FindUserByID(ctx context.Context, id string) (bool, *models.User, error) {
	return models.FindUserByID(ctx, id)
}
func (mongoStore) SetUserRole(ctx context.Context, userID primitive.ObjectID, role string, expected *time.Time) error {
	return models.SetUserRole(ctx, userID, role, expected)
}

// Service applies pending invites.
type Service struct {
	store   Store
	changes domain.ChangeLog
	expiry  time.Duration
	now     func() time.Time
}

// New builds the service. expiry is how long an invite stays usable (it also
// goes to Clerk as the invitation lifetime); 0 means never.
func New(changes domain.ChangeLog, expiry time.Duration) *Service {
	return NewWithStore(mongoStore{}, changes, expiry, time.Now)
}

// NewWithStore is New with injected dependencies (tests).
func NewWithStore(store Store, changes domain.ChangeLog, expiry time.Duration, now func() time.Time) *Service {
	return &Service{store: store, changes: changes, expiry: expiry, now: now}
}

// Expiry is the invite lifetime.
func (s *Service) Expiry() time.Duration { return s.expiry }

// maxRoleAttempts bounds the CAS retry when the user's role changes between
// read and write (e.g. the webhook and JWT paths racing).
const maxRoleAttempts = 3

// Apply gives user the role from their pending invite, if any, then marks the
// invite accepted. via names the trigger ("webhook" | "jwt") for logs and
// the change log. Order matters for idempotency: role first, then accepted —
// a crash in between leaves the invite pending, and the next sign-in re-runs
// a no-op role write and finishes it.
//
// A superuser is never demoted by an invite; the invite is still closed as
// accepted.
func (s *Service) Apply(ctx context.Context, user *models.User, via string) error {
	if user == nil || user.Email == "" {
		return nil // email not known yet — the other path will retry
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	found, inv, err := s.store.FindPendingByEmail(ctx, email)
	if err != nil || !found {
		return err
	}

	if s.expiry > 0 && s.now().Sub(inv.CreatedAt) > s.expiry {
		if err := s.store.MarkClosed(ctx, inv.ID, models.InviteStatusExpired); err != nil && !errors.Is(err, models.ErrInviteNotPending) {
			return err
		}
		log.Warn("staff invite expired before sign-in", "invite_id", inv.ID.Hex(), "email", email)
		return nil
	}

	before := user.Role
	if err := s.setRole(ctx, user, inv.Role); err != nil {
		return err
	}

	if err := s.store.MarkAccepted(ctx, inv.ID, user.ID.Hex()); err != nil {
		if errors.Is(err, models.ErrInviteNotPending) {
			return nil // the other path finished first
		}
		return err
	}
	log.Info("staff invite accepted", "invite_id", inv.ID.Hex(), "user_id", user.ID.Hex(), "role", inv.Role, "via", via)
	s.changes.Record(ctx, domain.ChangeEntry{
		Entity:   domain.EntityStaff,
		EntityID: user.ID.Hex(),
		Action:   domain.ActionUpdate,
		Actor:    domain.Actor{UserID: inv.InvitedByUserID, Email: inv.InvitedBy},
		Before:   map[string]any{"role": before},
		After:    map[string]any{"role": user.Role},
		Meta:     map[string]any{"inviteId": inv.ID.Hex(), "via": via},
	})
	return nil
}

// setRole writes role with the role_updated_at CAS, re-reading the user on a
// conflict. Leaves user.Role holding the stored role.
func (s *Service) setRole(ctx context.Context, user *models.User, role string) error {
	for attempt := 0; ; attempt++ {
		if user.Role == role || user.Role == models.RoleKeySuperuser {
			return nil
		}
		err := s.store.SetUserRole(ctx, user.ID, role, user.RoleUpdatedAt)
		if err == nil {
			user.Role = role
			return nil
		}
		if !errors.Is(err, models.ErrRoleConflictOnUser) || attempt+1 >= maxRoleAttempts {
			return err
		}
		found, fresh, ferr := s.store.FindUserByID(ctx, user.ID.Hex())
		if ferr != nil {
			return ferr
		}
		if !found {
			return errors.New("staff invite: user vanished during role apply")
		}
		*user = *fresh
	}
}
