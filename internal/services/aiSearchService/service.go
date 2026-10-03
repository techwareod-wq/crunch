// Package aiSearchService is WarehouseHub natural-language search (spec 05):
// a regex pre-parse, Claude extraction into the structured filters
// (forced tool use, hard deadline), the structured search (04), and the
// similar-matches fallback (Voyage embeddings + Atlas Vector Search, then a
// keyword $text search). It also keeps each live listing's embedding fresh.
package aiSearchService

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Request is POST /v1/public/search/ai.
type Request struct {
	Q string `json:"q"`
	// Location is the map centre / IP guess, used when the query names no
	// place (point and country only).
	Location  *domain.SearchLocation `json:"location,omitempty"`
	Page      int                    `json:"page,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
}

// Response is the structured response (04) plus the parse details.
type Response struct {
	domain.SearchResponse
	AI domain.SearchAI `json:"ai"`
}

// Async work.
const (
	// ProcessEmbed (re)embeds one live listing (D-083), dispatched by the
	// catalog on approve.
	ProcessEmbed pipeline.ProcessType = "aisearch.embed"
	// ProcessReembedAll dispatches ProcessEmbed for every live listing.
	ProcessReembedAll pipeline.ProcessType = "aisearch.reembed_all"
)

// EmbedPayload is the aisearch.embed body.
type EmbedPayload struct {
	WarehouseID string `json:"warehouseId"`
	LiveVersion int    `json:"liveVersion"`
}

// ReembedAllPayload is the aisearch.reembed_all body.
type ReembedAllPayload struct {
	Run string `json:"run"`
}

// EmbedKey is the message id of one embed job. run is empty for the
// approve dispatch and set for reembed-all (so a backfill isn't swallowed by
// the approve's key).
func EmbedKey(warehouseID string, liveVersion int, run string) string {
	if run == "" {
		return fmt.Sprintf("embed:%s:%d", warehouseID, liveVersion)
	}
	return fmt.Sprintf("embed:%s:%d:%s", warehouseID, liveVersion, run)
}

// AISearchService runs natural-language search.
type AISearchService interface {
	// Search never fails on an LLM, geocoder or vector outage (D-088); only
	// an empty query is a ValidationError.
	Search(ctx context.Context, req Request, v searchService.Viewer) (Response, error)

	// Embeddings (D-083).
	Embed(ctx context.Context, p EmbedPayload) error
	ReembedAll(ctx context.Context, p ReembedAllPayload) error
	// StartReembedAll queues a reembed-all run; returns its run id.
	StartReembedAll(ctx context.Context) (string, error)

	// SetLogger wires analytics (07); nil = no logging.
	SetLogger(l domain.SearchLogger)
}
