package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

func withListChangesSeam(t *testing.T, fn func(context.Context, models.ChangeLogFilter, int, int) ([]models.ChangeLogEntry, int64, error)) {
	t.Helper()
	orig := listChangeLog
	listChangeLog = fn
	t.Cleanup(func() { listChangeLog = orig })
}

func TestHandleAdminListChanges_ParsesFilters(t *testing.T) {
	var got models.ChangeLogFilter
	var gotPage, gotLimit int
	withListChangesSeam(t, func(_ context.Context, f models.ChangeLogFilter, page, limit int) ([]models.ChangeLogEntry, int64, error) {
		got, gotPage, gotLimit = f, page, limit
		return []models.ChangeLogEntry{{Entity: "warehouse"}}, 1, nil
	})

	actor := primitive.NewObjectID().Hex()
	r := httptest.NewRequest(http.MethodGet,
		"/v1/admin/changes?entity=warehouse&entityId=w1&actor="+actor+"&from=2026-10-01T00:00:00Z&page=2&limit=10", nil)
	rec := httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminListChanges, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got.Entity != "warehouse" || got.EntityID != "w1" || got.ActorUserID != actor || got.ActorEmail != "" {
		t.Errorf("filter = %+v", got)
	}
	if got.From == nil || !got.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || got.To != nil {
		t.Errorf("from/to = %v/%v", got.From, got.To)
	}
	if gotPage != 2 || gotLimit != 10 {
		t.Errorf("page/limit = %d/%d", gotPage, gotLimit)
	}
	var env struct{ Data listChangesResponse }
	err := json.Unmarshal(rec.Body.Bytes(), &env)
	if body := env.Data; err != nil || body.Total != 1 || len(body.Items) != 1 {
		t.Errorf("body = %+v err=%v", body, err)
	}
}

func TestHandleAdminListChanges_ActorEmailLowercased(t *testing.T) {
	var got models.ChangeLogFilter
	withListChangesSeam(t, func(_ context.Context, f models.ChangeLogFilter, _, _ int) ([]models.ChangeLogEntry, int64, error) {
		got = f
		return nil, 0, nil
	})
	rec := httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminListChanges, httptest.NewRequest(http.MethodGet, "/v1/admin/changes?actor=Ed@X.com", nil))
	if rec.Code != http.StatusOK || got.ActorEmail != "ed@x.com" || got.ActorUserID != "" {
		t.Errorf("status=%d filter=%+v", rec.Code, got)
	}
}

func TestHandleAdminListChanges_RejectsBadInput(t *testing.T) {
	withListChangesSeam(t, func(context.Context, models.ChangeLogFilter, int, int) ([]models.ChangeLogEntry, int64, error) {
		t.Fatal("list must not be called on bad input")
		return nil, 0, nil
	})
	for _, q := range []string{"entity=nope", "from=yesterday", "page=0", "limit=999"} {
		rec := httptest.NewRecorder()
		serveWithAppCtx(rec, HandleAdminListChanges, httptest.NewRequest(http.MethodGet, "/v1/admin/changes?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestHandleAdminChangeDetail(t *testing.T) {
	id := primitive.NewObjectID()
	orig := findChangeLogEntry
	t.Cleanup(func() { findChangeLogEntry = orig })
	findChangeLogEntry = func(_ context.Context, got primitive.ObjectID) (bool, *models.ChangeLogEntry, error) {
		if got != id {
			return false, nil, nil
		}
		return true, &models.ChangeLogEntry{ID: id, Before: bson.M{"name": "a"}, After: bson.M{"name": "b"}}, nil
	}

	rec := httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminChangeDetail, httptest.NewRequest(http.MethodGet, "/v1/admin/changes/detail?id="+id.Hex(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env struct{ Data map[string]any }
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	body := env.Data
	if body["before"].(map[string]any)["name"] != "a" || body["after"].(map[string]any)["name"] != "b" {
		t.Errorf("body = %v", body)
	}

	rec = httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminChangeDetail, httptest.NewRequest(http.MethodGet, "/v1/admin/changes/detail?id="+primitive.NewObjectID().Hex(), nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id: status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminChangeDetail, httptest.NewRequest(http.MethodGet, "/v1/admin/changes/detail?id=zzz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: status = %d, want 400", rec.Code)
	}
}
