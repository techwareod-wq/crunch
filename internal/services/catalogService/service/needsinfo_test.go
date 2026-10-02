package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func TestNeedsInfoSummaryAndList(t *testing.T) {
	h := newHarness()
	a, _ := h.live(t) // fullContent leaves hazmat unknown
	h.live(t)
	if _, err := h.svc.Open(ctx, editor, a.ID); err != nil {
		t.Fatal(err)
	}

	sum, err := h.svc.NeedsInfoSummary(ctx)
	if err != nil || len(sum) != 1 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
	if want := (dto.NeedsInfoKey{Key: "hazmat", Kind: dto.NeedsInfoNode, Name: "Hazmat", Count: 2}); sum[0] != want {
		t.Errorf("summary row = %+v", sum[0])
	}
	items, total, err := h.svc.NeedsInfoList(ctx, "hazmat", 1, 10)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list = %+v total %d, %v", items, total, err)
	}
	opened := 0
	for _, it := range items {
		if it.OpenRevision != nil {
			opened++
			if it.ID != a.ID.Hex() || it.OpenRevision.State != models.RevDraft {
				t.Errorf("open revision on the wrong row: %+v", it)
			}
		}
	}
	if opened != 1 {
		t.Errorf("rows with an open revision = %d", opened)
	}

	// Labels for a field key and for a key the tree no longer knows.
	snap := testSnapshot()
	if k, n := needsInfoLabel(snap, "cold_storage.temperature"); k != dto.NeedsInfoField || n != "Cold storage › Temperature" {
		t.Errorf("field label = %s %q", k, n)
	}
	if k, n := needsInfoLabel(snap, "gone"); k != dto.NeedsInfoNode || n != "gone" {
		t.Errorf("stale label = %s %q", k, n)
	}
}

func TestNeedsInfoAnswer(t *testing.T) {
	h := newHarness()
	a, _ := h.live(t)
	b, _ := h.live(t)
	c, _ := h.live(t)
	// c is in review: skipped.
	open, _ := h.svc.Open(ctx, editor, c.ID)
	if _, err := h.svc.Submit(ctx, editor, open.Revision.ID, ""); err != nil {
		t.Fatal(err)
	}
	h.cl.Entries = nil

	req := catalogService.AnswerRequest{Submit: true, Items: []catalogService.NeedsInfoAnswer{
		{WarehouseID: a.ID, Node: "hazmat", Status: domain.StatusNo},
		{WarehouseID: a.ID, Node: "cold_storage", Fields: map[string]*models.FieldValue{"temperature": fv(-20.0)}},
		{WarehouseID: b.ID, Node: "hazmat", Fields: map[string]*models.FieldValue{}},
		{WarehouseID: c.ID, Node: "hazmat", Status: domain.StatusNo},
	}}
	if _, _, err := h.svc.AnswerNeedsInfo(ctx, editor, req); err == nil {
		t.Fatal("empty fields without a status accepted")
	}
	req.Items[2] = catalogService.NeedsInfoAnswer{WarehouseID: b.ID, Node: "hazmat", Fields: map[string]*models.FieldValue{"x": fv(1.0)}}
	batch, items, err := h.svc.AnswerNeedsInfo(ctx, editor, req)
	if err != nil || batch == "" || len(items) != 3 {
		t.Fatalf("answer = %q %+v, %v", batch, items, err)
	}
	// a: both answers saved in one draft and submitted.
	if it := items[0]; !it.OK || !it.Submitted || it.WarehouseID != a.ID.Hex() {
		t.Errorf("a = %+v", it)
	}
	// b: field values on a node that isn't yes → refused before any draft opens.
	if it := items[1]; it.OK || it.RevisionID != "" {
		t.Errorf("b = %+v", it)
	}
	if w, _ := h.store.GetWarehouse(ctx, b.ID); w.OpenRevisionID != nil {
		t.Error("b got a draft")
	}
	// c: in review → skipped.
	if it := items[2]; it.OK || it.Code != catalogService.CodeInReview {
		t.Errorf("c = %+v", it)
	}

	r, _ := h.store.GetRevision(ctx, mustID(t, items[0].RevisionID))
	if r.State != models.RevInReview || r.BatchID != batch {
		t.Fatalf("a revision = %s batch %q", r.State, r.BatchID)
	}
	if _, has := r.Content.Attributes["hazmat"]; has {
		t.Error("hazmat not dropped")
	}
	if v := r.Content.Attributes.Value("cold_storage", "temperature"); v == nil || v.V != -20.0 {
		t.Errorf("temperature = %+v", v)
	}
	// Every log row of the batch carries the batchId (open is a plain create).
	for _, e := range h.cl.Entries {
		switch e.Action {
		case domain.ActionBulkAnswer, domain.ActionBulkSubmit:
			if e.Meta["batchId"] != batch {
				t.Errorf("%s row without the batch: %+v", e.Action, e.Meta)
			}
		}
	}

	// Approving clears the queue entry for a.
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if w, _ := h.store.GetWarehouse(ctx, a.ID); w.NeedsInfoCount != 0 {
		t.Errorf("a still needs info: %v", w.NeedsInfo)
	}
}

func mustID(t *testing.T, hex string) primitive.ObjectID {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
