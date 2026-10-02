package staffinvites

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
)

var testNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	invites  map[string]*models.StaffInvite // by email
	users    map[primitive.ObjectID]*models.User
	conflict int // SetUserRole returns a conflict this many times first
	roleSets int
}

func (f *fakeStore) FindPendingByEmail(_ context.Context, email string) (bool, *models.StaffInvite, error) {
	inv, ok := f.invites[email]
	if !ok || inv.Status != models.InviteStatusPending {
		return false, nil, nil
	}
	cp := *inv
	return true, &cp, nil
}
func (f *fakeStore) transition(id primitive.ObjectID, status string) error {
	for _, inv := range f.invites {
		if inv.ID == id {
			if inv.Status != models.InviteStatusPending {
				return models.ErrInviteNotPending
			}
			inv.Status = status
			return nil
		}
	}
	return models.ErrInviteNotPending
}
func (f *fakeStore) MarkAccepted(_ context.Context, id primitive.ObjectID, _ string) error {
	return f.transition(id, models.InviteStatusAccepted)
}
func (f *fakeStore) MarkClosed(_ context.Context, id primitive.ObjectID, status string) error {
	return f.transition(id, status)
}
func (f *fakeStore) FindUserByID(_ context.Context, id string) (bool, *models.User, error) {
	oid, _ := primitive.ObjectIDFromHex(id)
	u, ok := f.users[oid]
	if !ok {
		return false, nil, nil
	}
	cp := *u
	return true, &cp, nil
}
func (f *fakeStore) SetUserRole(_ context.Context, id primitive.ObjectID, role string, _ *time.Time) error {
	if f.conflict > 0 {
		f.conflict--
		return models.ErrRoleConflictOnUser
	}
	f.roleSets++
	f.users[id].Role = role
	return nil
}

func setup(role string, created time.Time) (*fakeStore, *models.User, *changelog.Memory, *Service) {
	u := &models.User{ID: primitive.NewObjectID(), Email: "New.Staff@X.com", Role: role}
	store := &fakeStore{
		invites: map[string]*models.StaffInvite{"new.staff@x.com": {
			ID: primitive.NewObjectID(), Email: "new.staff@x.com", Role: models.RoleKeyEditor,
			Status: models.InviteStatusPending, CreatedAt: created, InvitedBy: "boss@x.com",
		}},
		users: map[primitive.ObjectID]*models.User{u.ID: {ID: u.ID, Email: u.Email, Role: role}},
	}
	mem := &changelog.Memory{}
	return store, u, mem, NewWithStore(store, mem, 30*24*time.Hour, func() time.Time { return testNow })
}

// Webhook then JWT (replay): role applied once, invite accepted once, one
// change_log entry.
func TestApply_IdempotentAcrossPaths(t *testing.T) {
	store, u, mem, svc := setup(models.RoleKeyUser, testNow.Add(-time.Hour))
	if err := svc.Apply(context.Background(), u, "webhook"); err != nil {
		t.Fatal(err)
	}
	again := *store.users[u.ID]
	if err := svc.Apply(context.Background(), &again, "jwt"); err != nil {
		t.Fatal(err)
	}
	if got := store.users[u.ID].Role; got != models.RoleKeyEditor {
		t.Errorf("role = %q, want editor", got)
	}
	if store.invites["new.staff@x.com"].Status != models.InviteStatusAccepted {
		t.Errorf("invite status = %q", store.invites["new.staff@x.com"].Status)
	}
	if store.roleSets != 1 || len(mem.Entries) != 1 {
		t.Errorf("roleSets=%d changeEntries=%d, want 1/1", store.roleSets, len(mem.Entries))
	}
	if e := mem.Entries[0]; e.Actor.Email != "boss@x.com" || e.Meta["via"] != "webhook" {
		t.Errorf("change entry = %+v", e)
	}
}

// Crash between role write and accept: the next run finishes the invite.
func TestApply_ResumesAfterPartialRun(t *testing.T) {
	store, u, _, svc := setup(models.RoleKeyEditor, testNow.Add(-time.Hour)) // role already set
	if err := svc.Apply(context.Background(), u, "jwt"); err != nil {
		t.Fatal(err)
	}
	if store.roleSets != 0 || store.invites["new.staff@x.com"].Status != models.InviteStatusAccepted {
		t.Errorf("roleSets=%d status=%q", store.roleSets, store.invites["new.staff@x.com"].Status)
	}
}

func TestApply_RetriesRoleConflict(t *testing.T) {
	store, u, _, svc := setup(models.RoleKeyUser, testNow.Add(-time.Hour))
	store.conflict = 1
	if err := svc.Apply(context.Background(), u, "webhook"); err != nil {
		t.Fatal(err)
	}
	if store.users[u.ID].Role != models.RoleKeyEditor {
		t.Errorf("role = %q after conflict retry", store.users[u.ID].Role)
	}
}

func TestApply_NeverDemotesSuperuser(t *testing.T) {
	store, u, _, svc := setup(models.RoleKeySuperuser, testNow.Add(-time.Hour))
	if err := svc.Apply(context.Background(), u, "webhook"); err != nil {
		t.Fatal(err)
	}
	if store.users[u.ID].Role != models.RoleKeySuperuser || store.roleSets != 0 {
		t.Errorf("superuser demoted: role=%q", store.users[u.ID].Role)
	}
}

func TestApply_ExpiredInvite(t *testing.T) {
	store, u, mem, svc := setup(models.RoleKeyUser, testNow.Add(-31*24*time.Hour))
	if err := svc.Apply(context.Background(), u, "webhook"); err != nil {
		t.Fatal(err)
	}
	if store.users[u.ID].Role != models.RoleKeyUser || store.invites["new.staff@x.com"].Status != models.InviteStatusExpired || len(mem.Entries) != 0 {
		t.Errorf("expired invite applied: role=%q status=%q", store.users[u.ID].Role, store.invites["new.staff@x.com"].Status)
	}
}

func TestApply_NoInviteOrNoEmail(t *testing.T) {
	store, _, _, svc := setup(models.RoleKeyUser, testNow)
	other := &models.User{ID: primitive.NewObjectID(), Email: "someone@else.com", Role: models.RoleKeyUser}
	if err := svc.Apply(context.Background(), other, "jwt"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Apply(context.Background(), &models.User{ID: primitive.NewObjectID()}, "jwt"); err != nil {
		t.Fatal(err)
	}
	if store.roleSets != 0 {
		t.Error("role set without a matching invite")
	}
}
