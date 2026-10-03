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

func TestNormalizeFeatures(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
		ok   bool
	}{
		{[]string{"enquiries", "search", "search"}, []string{"search", "enquiries"}, true},
		{nil, nil, true},
		{[]string{""}, nil, false},
		{[]string{"bogus"}, nil, false},
	}
	for _, c := range cases {
		got, ok := normalizeFeatures(c.in)
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("normalizeFeatures(%v) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSetUserFeatures(t *testing.T) {
	target := &models.User{ID: primitive.NewObjectID(), Email: "t@x.com", Role: models.RoleUser}
	withFindUserSeam(t, func(context.Context, string) (bool, *models.User, error) { return true, target, nil })
	var got []string
	orig := setUserFeatures
	t.Cleanup(func() { setUserFeatures = orig })
	setUserFeatures = func(_ context.Context, _ primitive.ObjectID, features []string, _ *time.Time) error {
		got = features
		return nil
	}
	serve := func(req adminSetUserFeaturesRequest) int {
		rec := httptest.NewRecorder()
		HandleAdminSetUserFeatures(rec, deserReq(callerUser(models.RoleSuperuser), req))
		return rec.Code
	}

	if code := serve(adminSetUserFeaturesRequest{TargetUserID: target.ID.Hex(), Features: []string{"ai_search", "search"}}); code != http.StatusOK {
		t.Fatalf("grant: %d", code)
	}
	if !slices.Equal(got, []string{"search", "ai_search"}) {
		t.Fatalf("wrote %v", got)
	}
	if code := serve(adminSetUserFeaturesRequest{TargetUserID: target.ID.Hex(), Features: []string{"bogus"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown feature: %d", code)
	}

	target.Role = models.RoleSuperuser
	if code := serve(adminSetUserFeaturesRequest{TargetUserID: target.ID.Hex()}); code != http.StatusForbidden {
		t.Fatalf("edit superuser: %d", code)
	}

	target.Role = models.RoleUser
	setUserFeatures = func(context.Context, primitive.ObjectID, []string, *time.Time) error {
		return models.ErrRoleConflictOnUser
	}
	if code := serve(adminSetUserFeaturesRequest{TargetUserID: target.ID.Hex()}); code != http.StatusConflict {
		t.Fatalf("stale write: %d", code)
	}
}
