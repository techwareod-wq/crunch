package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
)

type fakeInviteClerk struct {
	createErr error
	created   []string
	revoked   []string
}

func (f *fakeInviteClerk) DeleteUser(context.Context, string) error { return nil }
func (f *fakeInviteClerk) CreateInvitation(_ context.Context, email string, _ map[string]any, _ string, _ int) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created = append(f.created, email)
	return "inv_123", nil
}
func (f *fakeInviteClerk) RevokeInvitation(_ context.Context, id string) error {
	f.revoked = append(f.revoked, id)
	return nil
}

// withStaffSeams swaps every staff seam for in-memory fakes; invites is the
// fake collection keyed by id.
func withStaffSeams(t *testing.T, existingUser bool) (map[primitive.ObjectID]*models.StaffInvite, *changelog.Memory) {
	t.Helper()
	invites := map[primitive.ObjectID]*models.StaffInvite{}
	mem := &changelog.Memory{}
	o1, o2, o3, o4, o5, o6, o7 := insertStaffInvite, setStaffInviteClerkID, deleteStaffInvite, findStaffInviteByID, markStaffInviteClosed, findUserByEmail, staffChangeLog
	t.Cleanup(func() {
		insertStaffInvite, setStaffInviteClerkID, deleteStaffInvite, findStaffInviteByID, markStaffInviteClosed, findUserByEmail, staffChangeLog = o1, o2, o3, o4, o5, o6, o7
	})
	insertStaffInvite = func(_ context.Context, inv *models.StaffInvite) error {
		for _, other := range invites {
			if other.Email == inv.Email && other.Status == models.InviteStatusPending {
				return models.ErrInvitePending
			}
		}
		inv.ID, inv.Status = primitive.NewObjectID(), models.InviteStatusPending
		invites[inv.ID] = inv
		return nil
	}
	setStaffInviteClerkID = func(_ context.Context, id primitive.ObjectID, c string) error {
		invites[id].ClerkInvitationID = c
		return nil
	}
	deleteStaffInvite = func(_ context.Context, id primitive.ObjectID) error { delete(invites, id); return nil }
	findStaffInviteByID = func(_ context.Context, id primitive.ObjectID) (bool, *models.StaffInvite, error) {
		inv, ok := invites[id]
		if !ok {
			return false, nil, nil
		}
		cp := *inv
		return true, &cp, nil
	}
	markStaffInviteClosed = func(_ context.Context, id primitive.ObjectID, status string) error {
		if invites[id].Status != models.InviteStatusPending {
			return models.ErrInviteNotPending
		}
		invites[id].Status = status
		return nil
	}
	findUserByEmail = func(context.Context, string) (bool, *models.User, error) { return existingUser, nil, nil }
	staffChangeLog = mem
	return invites, mem
}

func serveStaff(handler http.HandlerFunc, clerk *fakeInviteClerk, body any) *httptest.ResponseRecorder {
	appCtx := adminPaginationAppCtx()
	appCtx.InternalServices.ClerkAccounts = clerk
	appCtx.Config.Values.Admin.StaffInviteExpiryDays = 30
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx := context.WithValue(r.Context(), middleware.UserContextKey, &models.User{ID: primitive.NewObjectID(), Email: "Boss@X.com", Role: models.RoleKeySuperuser})
	ctx = context.WithValue(ctx, middleware.DeserializerContextKey, body)
	rec := httptest.NewRecorder()
	appCtx.Middleware()(handler).ServeHTTP(rec, r.WithContext(ctx))
	return rec
}

func TestStaffInvite_CreateAndDuplicate(t *testing.T) {
	invites, mem := withStaffSeams(t, false)
	clerk := &fakeInviteClerk{}

	rec := serveStaff(HandleAdminStaffInvite, clerk, adminStaffInviteRequest{Email: " New@X.com ", Role: models.RoleKeyEditor})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(invites) != 1 || len(clerk.created) != 1 || clerk.created[0] != "new@x.com" {
		t.Fatalf("invites=%d clerk=%v", len(invites), clerk.created)
	}
	for _, inv := range invites {
		if inv.ClerkInvitationID != "inv_123" || inv.InvitedBy != "boss@x.com" {
			t.Errorf("invite = %+v", inv)
		}
	}
	if len(mem.Entries) != 1 {
		t.Errorf("change entries = %d", len(mem.Entries))
	}

	rec = serveStaff(HandleAdminStaffInvite, clerk, adminStaffInviteRequest{Email: "new@x.com", Role: models.RoleKeyApprover})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate pending: status = %d, want 409", rec.Code)
	}
}

func TestStaffInvite_Rejections(t *testing.T) {
	withStaffSeams(t, false)
	for _, req := range []adminStaffInviteRequest{
		{Email: "a@x.com", Role: models.RoleKeySuperuser},
		{Email: "a@x.com", Role: models.RoleKeyUser},
		{Email: "nope", Role: models.RoleKeyEditor},
		{Email: "A <a@x.com>", Role: models.RoleKeyEditor},
	} {
		if rec := serveStaff(HandleAdminStaffInvite, &fakeInviteClerk{}, req); rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: status = %d, want 400", req, rec.Code)
		}
	}

	withStaffSeams(t, true)
	if rec := serveStaff(HandleAdminStaffInvite, &fakeInviteClerk{}, adminStaffInviteRequest{Email: "a@x.com", Role: models.RoleKeyEditor}); rec.Code != http.StatusConflict {
		t.Errorf("existing user: status = %d, want 409", rec.Code)
	}
}

func TestStaffInvite_ClerkFailureRollsBack(t *testing.T) {
	invites, _ := withStaffSeams(t, false)
	rec := serveStaff(HandleAdminStaffInvite, &fakeInviteClerk{createErr: errors.New("clerk down")}, adminStaffInviteRequest{Email: "a@x.com", Role: models.RoleKeyEditor})
	if rec.Code != http.StatusBadGateway || len(invites) != 0 {
		t.Errorf("status=%d invites=%d, want 502 and no invite left", rec.Code, len(invites))
	}
}

func TestStaffInvite_Revoke(t *testing.T) {
	invites, _ := withStaffSeams(t, false)
	clerk := &fakeInviteClerk{}
	serveStaff(HandleAdminStaffInvite, clerk, adminStaffInviteRequest{Email: "a@x.com", Role: models.RoleKeyEditor})
	var id primitive.ObjectID
	for k := range invites {
		id = k
	}

	if rec := serveStaff(HandleAdminRevokeStaffInvite, clerk, adminRevokeStaffInviteRequest{ID: id.Hex()}); rec.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d", rec.Code)
	}
	if invites[id].Status != models.InviteStatusRevoked || len(clerk.revoked) != 1 {
		t.Errorf("status=%q revoked=%v", invites[id].Status, clerk.revoked)
	}
	if rec := serveStaff(HandleAdminRevokeStaffInvite, clerk, adminRevokeStaffInviteRequest{ID: id.Hex()}); rec.Code != http.StatusConflict {
		t.Errorf("second revoke: status = %d, want 409", rec.Code)
	}
}
