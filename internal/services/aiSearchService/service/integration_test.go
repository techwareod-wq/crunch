package service

// Real-Mongo check of the embed job's store (skipped without
// WH_MONGO_TEST_URI; see searchService's integration_test.go).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func TestIntegrationEmbed(t *testing.T) {
	uri := os.Getenv("WH_MONGO_TEST_URI")
	if uri == "" {
		t.Skip("WH_MONGO_TEST_URI not set")
	}
	if err := models.Connect(uri, fmt.Sprintf("wh_ai_it_%d", time.Now().UnixNano())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.DB.Drop(context.Background()) })

	w := &models.Warehouse{ShortID: "abc12345", Slug: "s", SlugHistory: []string{}, Status: models.WarehouseLive, LiveVersion: 2,
		Name: "Hub", Live: &models.ListingContent{Attributes: models.Attributes{domain.RootKey: {Status: domain.StatusYes}}}}
	if err := models.InsertWarehouse(ctx, w); err != nil {
		t.Fatal(err)
	}
	h := newHarness()
	h.svc.store = store.NewStore()

	if err := h.svc.Embed(ctx, aiSearchService.EmbedPayload{WarehouseID: w.ID.Hex(), LiveVersion: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := models.FindWarehouseByID(ctx, w.ID)
	if len(got.Embedding) != 2 || got.EmbeddingHash == "" {
		t.Fatalf("embedding = %v hash %q", got.Embedding, got.EmbeddingHash)
	}
	// Same text: no second embed call. Stale version: nothing written.
	h.svc.Embed(ctx, aiSearchService.EmbedPayload{WarehouseID: w.ID.Hex(), LiveVersion: 2})
	if ok, _ := models.SetWarehouseEmbedding(ctx, w.ID, 1, []float32{9}, "x"); ok || h.emb.calls != 1 {
		t.Errorf("stale write ok=%v calls=%d", ok, h.emb.calls)
	}
	refs, err := models.ListLiveWarehouseRefs(ctx, [12]byte{}, 10)
	if err != nil || len(refs) != 1 || refs[0].LiveVersion != 2 {
		t.Errorf("refs = %+v %v", refs, err)
	}
}
