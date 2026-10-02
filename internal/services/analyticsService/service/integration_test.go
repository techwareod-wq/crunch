package service

// Integration test against a real MongoDB (spec 07 Tests). Skipped unless
// WH_MONGO_TEST_URI is set, e.g.
//
//	mongod --dbpath /tmp/whdb --port 27999
//	WH_MONGO_TEST_URI=mongodb://127.0.0.1:27999 go test ./internal/services/analyticsService/... -run Integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func connectTestDB(t *testing.T) {
	t.Helper()
	uri := os.Getenv("WH_MONGO_TEST_URI")
	if uri == "" {
		t.Skip("WH_MONGO_TEST_URI not set")
	}
	if err := models.Connect(uri, fmt.Sprintf("wh_analytics_it_%d", time.Now().UnixNano())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.DB.Drop(context.Background()) })
	for _, ensure := range []func(context.Context) error{
		models.EnsureSearchEventIndexes, models.EnsureSearchDailyIndexes, models.EnsureEnquiryIndexes,
	} {
		if err := ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationTTLIndex(t *testing.T) {
	connectTestDB(t)
	cur, err := models.Collection("search_events").Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range idx {
		if keys, _ := i["key"].(bson.M); keys != nil && len(keys) == 1 && keys["at"] != nil {
			found = i["expireAfterSeconds"] == int32(90*24*3600)
		}
	}
	if !found {
		t.Errorf("no 90-day TTL index on search_events.at: %v", idx)
	}
}

func TestIntegrationLogRollupDashboards(t *testing.T) {
	connectTestDB(t)
	s := &svc{store: store.NewStore(), loc: ist, now: func() time.Time { return day.AddDate(0, 0, 1).Add(9 * time.Hour) }}

	uid := primitive.NewObjectID()
	var first string
	for i, ev := range []domain.SearchEvent{
		{Source: "ai", Total: 3, Filters: domain.SearchFilters{Text: "Cold storage Pune"}, AI: &domain.SearchAI{Parsed: true, LatencyMs: 800}, UserID: uid.Hex()},
		{Source: "ai", Total: 0, Filters: domain.SearchFilters{Text: "cold  storage pune", Location: &domain.SearchLocation{Place: "Pune", Country: "IN"}}, AI: &domain.SearchAI{Parsed: false, LatencyMs: 2200}},
		{Source: "structured", Total: 0, Filters: domain.SearchFilters{Text: "cold storage pune"}, Staff: true},
	} {
		ev.SearchID = primitive.NewObjectID().Hex()
		ev.At = at(i + 1)
		ev.Page = 1
		if i == 0 {
			first = ev.SearchID
		}
		s.Log(ctx, ev)
	}
	sid, _ := primitive.ObjectIDFromHex(first)
	e := &models.Enquiry{UserID: uid, Name: "A", Email: "a@x.com", Phone: "1", PhoneE164: "+1", Message: "need space",
		Country: "IN", Status: models.EnquiryNew, SearchID: &sid, IdempotencyKey: "k", CreatedAt: at(5), UpdatedAt: at(5),
		Notes: []models.EnquiryNote{}, History: []models.EnquiryChange{}}
	if err := models.InsertEnquiry(ctx, e); err != nil {
		t.Fatal(err)
	}

	r := analyticsService.Range{From: "2026-09-30", To: "2026-09-30"}
	live, err := s.Overview(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RollupDaily(ctx, analyticsService.RollupPayload{}); err != nil {
		t.Fatal(err)
	}
	rolled, err := s.Overview(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if live.Totals != rolled.Totals || rolled.Totals.Searches != 2 || rolled.Totals.AIParseFailures != 1 ||
		rolled.Totals.Enquiries != 1 || rolled.Totals.EnquiriesFromSearch != 1 {
		t.Errorf("live %+v\nrolled %+v", live.Totals, rolled.Totals)
	}
	if through, _ := models.GetCounter(ctx, models.CounterAnalyticsRolledThrough); through != 20260930 {
		t.Errorf("watermark = %d", through)
	}

	top, err := s.Top(ctx, r, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Items) != 1 || top.Items[0].Searches != 2 || top.Items[0].ZeroResults != 1 || top.Items[0].Enquiries != 1 {
		t.Errorf("top = %+v", top.Items)
	}
	zero, _ := s.ZeroResults(ctx, r, 10)
	if len(zero.Items) != 1 || zero.Items[0].Place != "Pune" {
		t.Errorf("zero = %+v", zero.Items)
	}
	logged, total, err := s.Events(ctx, models.SearchEventFilter{Q: "COLD Storage", Zero: true}, 1, 10)
	if err != nil || total != 2 || len(logged) != 2 {
		t.Errorf("log = %d (%v)", total, err)
	}

	n, err := s.Cleaner().DeleteUserData(ctx, uid)
	if err != nil || n != 1 {
		t.Errorf("cleaner = %d, %v", n, err)
	}
	ev, _ := models.FindSearchEventByID(ctx, sid)
	if ev == nil || ev.UserID != nil {
		t.Errorf("user id kept: %+v", ev)
	}
}
