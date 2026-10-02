package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

func TestHandleAdminListStaff_ReturnsEffectivePermissions(t *testing.T) {
	orig := listStaff
	listStaff = func(context.Context) ([]models.User, error) {
		return []models.User{
			{ID: primitive.NewObjectID(), Email: "ed@x.com", Name: "Ed", Role: models.RoleAdmin, Permissions: []string{"editor"}},
			{ID: primitive.NewObjectID(), Email: "root@x.com", Role: models.RoleSuperuser},
		}, nil
	}
	t.Cleanup(func() { listStaff = orig })

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/admin/staff", nil)
	(&config.AppContext{}).Middleware()(http.HandlerFunc(HandleAdminListStaff)).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Items []staffMember `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	items := body.Data.Items
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if got := items[0].Permissions; len(got) != 2 || got[0] != "admin" || got[1] != "editor" {
		t.Errorf("admin perms = %v, want [admin editor]", got)
	}
	if got := items[1].Permissions; len(got) != 5 {
		t.Errorf("superuser perms = %v, want all 5", got)
	}
}
