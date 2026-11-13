package service

// AI search over the Groq provider against a mock Groq server: a parse, and
// every failure falling back to the basic search with its ai.notes entry.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/impl/llm/groq"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/searchService"
)

// groqReply is a JSON-mode reply carrying the set_filters arguments.
func groqReply(args string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": args}}}})
	return string(b)
}

func TestAISearchOverGroq(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		slow   bool
		note   string
	}{
		"success":   {status: 200, body: groqReply(`{"location":{"text":"Pune","kind":"place"},"chips":["cold_storage"],"industries":[],"unmapped":[],"confidence":"high"}`)},
		"malformed": {status: 200, body: groqReply(`{"chips": ["cold`), note: "llm_malformed"},
		"429":       {status: 429, body: `{"error":{"message":"Rate limit reached"}}`, note: "llm_rate_limited"},
		"503":       {status: 503, body: `{"error":{"message":"over capacity"}}`, note: "llm_unavailable"},
		"timeout":   {slow: true, note: "llm_timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.slow {
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			h := newHarness() // 50 ms deadline
			h.svc.llm = groq.New("k", srv.URL, "", groq.ModeJSON, 0)

			resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "cold storage in Pune"}, searchService.Viewer{})
			if err != nil {
				t.Fatal(err)
			}
			f := h.search.filters[0]
			if tc.note == "" {
				if !resp.AI.Parsed || f.Location.Place != "Pune" || !slices.Equal(f.Chips, []string{"cold_storage"}) {
					t.Errorf("ai = %+v filters = %+v", resp.AI, f)
				}
				return
			}
			if resp.AI.Parsed || !slices.Contains(resp.AI.Notes, "basic_search") || !slices.Contains(resp.AI.Notes, tc.note) {
				t.Errorf("notes = %v", resp.AI.Notes)
			}
			if !slices.Equal(f.Chips, []string{"cold_storage"}) {
				t.Errorf("basic filters = %+v", f)
			}
		})
	}
}
