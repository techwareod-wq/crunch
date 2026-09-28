package service

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

var statusNow = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

func statusUser(ent *models.AppEntitlement) *models.User {
	u := &models.User{ID: primitive.NewObjectID()}
	if ent != nil {
		u.Entitlements = map[string]models.AppEntitlement{models.AppIDIndexly: *ent}
	}
	return u
}

func TestBuildAppsAccess(t *testing.T) {
	future := statusNow.Add(24 * time.Hour)
	past := statusNow.Add(-24 * time.Hour)

	s := newTestService(t, &mockStore{}) // only the plans cache is used here

	t.Run("no entitlement → none with empty features", func(t *testing.T) {
		apps := buildAppsAccess(statusUser(nil), s.plans, statusNow)
		got, ok := apps[models.AppIDIndexly] // indexly present via the plan catalog union
		if !ok {
			t.Fatal("catalog app missing from Apps union")
		}
		if got.Status != "none" || got.ValidTill != nil {
			t.Errorf("got %+v, want none with nil validTill", got)
		}
		if got.Features == nil || len(got.Features) != 0 {
			t.Errorf("none must serialize an EMPTY feature list, got %#v", got.Features)
		}
	})

	t.Run("valid sub → status, tier, validTill, features", func(t *testing.T) {
		apps := buildAppsAccess(statusUser(&models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_1", ValidTill: future, Ver: 1,
		}), s.plans, statusNow)
		got := apps[models.AppIDIndexly]
		if got.Status != models.SubStatusActive || got.Tier != "pro" {
			t.Errorf("got %+v", got)
		}
		if got.ValidTill == nil || !got.ValidTill.Equal(future) {
			t.Errorf("validTill = %v, want %v", got.ValidTill, future)
		}
		if len(got.Features) == 0 {
			t.Error("valid sub must carry the effective feature set")
		}
	})

	t.Run("expired sub → expired, empty features, boundary kept", func(t *testing.T) {
		apps := buildAppsAccess(statusUser(&models.AppEntitlement{
			Status: models.SubStatusActive, PriceID: "pri_1", ValidTill: past, Ver: 1,
		}), s.plans, statusNow)
		got := apps[models.AppIDIndexly]
		if got.Status != "expired" {
			t.Errorf("status = %q, want expired (derived from valid_till)", got.Status)
		}
		if got.ValidTill == nil || !got.ValidTill.Equal(past) {
			t.Errorf("expired must keep the past boundary, got %v", got.ValidTill)
		}
		if len(got.Features) != 0 {
			t.Errorf("expired features = %v, want empty", got.Features)
		}
	})

	t.Run("entitlement key outside catalog still listed", func(t *testing.T) {
		u := &models.User{ID: primitive.NewObjectID(), Entitlements: map[string]models.AppEntitlement{
			"app9": {Status: models.SubStatusActive, PriceID: "pri_unknown", ValidTill: future, Ver: 1},
		}}
		apps := buildAppsAccess(u, s.plans, statusNow)
		if _, ok := apps["app9"]; !ok {
			t.Error("union must include entitlement keys with no plan docs")
		}
	})
}
