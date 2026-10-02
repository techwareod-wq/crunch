package service

// Integration test against a real MongoDB (spec 06). Skipped unless
// WH_MONGO_TEST_URI is set, e.g.
//
//	mongod --dbpath /tmp/whdb --port 27999
//	WH_MONGO_TEST_URI=mongodb://127.0.0.1:27999 go test ./internal/services/enquiryService/... -run Integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
)

func TestIntegrationInbox(t *testing.T) {
	uri := os.Getenv("WH_MONGO_TEST_URI")
	if uri == "" {
		t.Skip("WH_MONGO_TEST_URI not set")
	}
	ctx := context.Background()
	if err := models.Connect(uri, fmt.Sprintf("wh_enquiries_it_%d", time.Now().UnixNano())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.DB.Drop(context.Background()) })
	if err := models.EnsureEnquiryIndexes(ctx); err != nil {
		t.Fatal(err)
	}

	visitorUser := &models.User{ClerkID: "c1", Email: "v@x.com", Name: "Vee", Role: models.RoleUser}
	editor := &models.User{ClerkID: "c2", Email: "ed@x.com", Role: models.RoleAdmin, Permissions: []string{string(authz.PermEditor)}}
	for _, u := range []*models.User{visitorUser, editor} {
		if err := models.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(store.NewStore(), &changelog.Memory{}, config.WarehouseHubValues{}, config.AWSConfig{}).(*svc)

	req := validReq("idem-1")
	res, err := s.Submit(ctx, visitorUser, req)
	if err != nil || !res.Created {
		t.Fatalf("submit = %+v, %v", res, err)
	}
	again, err := s.Submit(ctx, visitorUser, req)
	if err != nil || again.ID != res.ID || again.Created {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if _, u, _ := models.FindUserByID(ctx, visitorUser.ID.Hex()); u.PhoneE164 != "+919820012345" || u.Company != "Rao Logistics" {
		t.Errorf("profile = %+v", u)
	}

	id, _ := primitive.ObjectIDFromHex(res.ID)
	e, err := models.FindEnquiryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The client echoes updatedAt through JSON; Mongo keeps milliseconds.
	got, err := s.SetStatus(ctx, staff, enquiryService.StatusRequest{ID: id, Status: models.EnquiryContacted, ExpectedUpdatedAt: e.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, staff, enquiryService.StatusRequest{ID: id, Status: models.EnquiryNew, ExpectedUpdatedAt: e.UpdatedAt}); !errors.Is(err, enquiryService.ErrStale) {
		t.Errorf("stale: err = %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	got, err = s.Assign(ctx, staff, enquiryService.AssignRequest{ID: id, AssigneeUserID: &editor.ID, ExpectedUpdatedAt: got.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(ctx, staff, enquiryService.NoteRequest{ID: id, Body: "called"}); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.List(ctx, models.EnquiryFilter{Q: "RAO log", Assignee: &editor.ID, Status: models.EnquiryContacted}, 1, 10)
	if err != nil || total != 1 || items[0].ID != id || items[0].Notes != nil {
		t.Errorf("list = %d %+v %v", total, items, err)
	}
	d, err := s.Detail(ctx, id)
	if err != nil || len(d.Enquiry.Notes) != 1 || len(d.Enquiry.History) != 2 {
		t.Errorf("detail = %+v, %v", d, err)
	}

	if n, err := s.Cleaner().DeleteUserData(ctx, visitorUser.ID); err != nil || n != 1 {
		t.Errorf("cleaner = %d, %v", n, err)
	}
	if _, total, _ := s.List(ctx, models.EnquiryFilter{}, 1, 10); total != 0 {
		t.Errorf("deleted enquiry still listed")
	}
}
