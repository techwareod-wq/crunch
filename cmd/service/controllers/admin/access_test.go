package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

func TestNormalizeAccess(t *testing.T) {
	cases := []struct {
		role  string
		perms []string
		want  []string
		ok    bool
	}{
		{"admin", []string{"approver", "editor", "editor"}, []string{"editor", "approver"}, true},
		{"admin", nil, nil, true},
		{"user", nil, nil, true},
		{"user", []string{"editor"}, nil, false},
		{"superuser", nil, nil, false},
		{"admin", []string{"superuser"}, nil, false},
		{"approver", nil, nil, false},
	}
	for _, c := range cases {
		got, ok := normalizeAccess(c.role, c.perms)
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("normalizeAccess(%q, %v) = %v, %v; want %v, %v", c.role, c.perms, got, ok, c.want, c.ok)
		}
	}
}

func TestSetUserAccess(t *testing.T) {
	target := &models.User{ID: primitive.NewObjectID(), Email: "t@x.com", Role: models.RoleUser}
	withFindUserSeam(t, func(context.Context, string) (bool, *models.User, error) { return true, target, nil })
	var gotRole string
	var gotPerms []string
	orig := setUserAccess
	t.Cleanup(func() { setUserAccess = orig })
	setUserAccess = func(_ context.Context, _ primitive.ObjectID, role string, perms []string, _ *time.Time) error {
		gotRole, gotPerms = role, perms
		return nil
	}
	serve := func(req adminSetUserAccessRequest) int {
		rec := httptest.NewRecorder()
		HandleAdminSetUserAccess(rec, deserReq(callerUser(models.RoleSuperuser), req))
		return rec.Code
	}

	if code := serve(adminSetUserAccessRequest{TargetUserID: target.ID.Hex(), Role: "admin", Permissions: []string{"editor"}}); code != http.StatusOK {
		t.Fatalf("grant editor: %d", code)
	}
	if gotRole != "admin" || !slices.Equal(gotPerms, []string{"editor"}) {
		t.Fatalf("wrote %s %v", gotRole, gotPerms)
	}
	if code := serve(adminSetUserAccessRequest{TargetUserID: target.ID.Hex(), Role: "superuser"}); code != http.StatusBadRequest {
		t.Fatalf("mint superuser via API: %d", code)
	}

	target.Role = models.RoleSuperuser
	if code := serve(adminSetUserAccessRequest{TargetUserID: target.ID.Hex(), Role: "user"}); code != http.StatusForbidden {
		t.Fatalf("demote superuser via API: %d", code)
	}

	target.Role = models.RoleAdmin
	setUserAccess = func(context.Context, primitive.ObjectID, string, []string, *time.Time) error {
		return models.ErrRoleConflictOnUser
	}
	if code := serve(adminSetUserAccessRequest{TargetUserID: target.ID.Hex(), Role: "user"}); code != http.StatusConflict {
		t.Fatalf("stale write: %d", code)
	}
}
