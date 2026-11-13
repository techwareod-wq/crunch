package service

// Live Groq check of the parsing step (prompt → set_filters → Go
// validation), with no HTTP route and no login. Skipped unless GROQ_API_KEY
// is set. The vocabulary is read (read-only) from the LOCAL Mongo seed:
// WH_LOCAL_MONGO_URI (default mongodb://127.0.0.1:27017, local hosts only)
// and WH_LOCAL_MONGO_DB (default crunch_local_seed).
//
//	GROQ_API_KEY=... go test ./internal/services/aiSearchService/service -run TestLiveGroqParse -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/groq"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	attributestore "github.com/atharva-ng/crunch/internal/services/attributeService/store"
	searchsvc "github.com/atharva-ng/crunch/internal/services/searchService/service"
	searchstore "github.com/atharva-ng/crunch/internal/services/searchService/store"
)

var liveQueries = []string{
	"I want 1000 sq ft bonded in Mumbai",
	"50000 sqft general near Mundra",
	"200 pallets cold storage in Pune",
	"cold storage in Pune under ₹30 per sqft",
	"pharma warehouse near Hyderabad at least 20,000 sqft",
	"2 lakh sqft near Delhi airport",
	"asdf qwerty",
}

// recordingLLM keeps the last response's usage and latency.
type recordingLLM struct {
	interfaces.LlmService
	last    *dto.PromptResponse
	latency time.Duration
}

func (r *recordingLLM) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	start := time.Now()
	resp, err := r.LlmService.Prompt(ctx, req)
	r.last, r.latency = resp, time.Since(start)
	return resp, err
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestLiveGroqParse(t *testing.T) {
	key := os.Getenv("GROQ_API_KEY")
	if key == "" {
		t.Skip("GROQ_API_KEY not set")
	}
	uri := envOr("WH_LOCAL_MONGO_URI", "mongodb://127.0.0.1:27017")
	if u, err := url.Parse(uri); err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatalf("WH_LOCAL_MONGO_URI must point at a local Mongo, got host %q", u.Hostname())
	}
	if err := models.Connect(uri, envOr("WH_LOCAL_MONGO_DB", "crunch_local_seed")); err != nil {
		t.Skipf("local Mongo not reachable (start it with scripts/local/run.sh): %v", err)
	}
	snap, err := attributestore.NewStore().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	search := searchsvc.NewService(searchstore.NewStore(), staticRules{snap}, nil, config.WarehouseHubValues{}, config.AWSConfig{})
	cat, _ := search.Catalog("IN", false)
	p := buildPrompt(snap, cat)

	deadline := 2200
	if ms, err := strconv.Atoi(os.Getenv("AISEARCH_TIMEOUT_MS")); err == nil && ms > 0 {
		deadline = ms
	}
	model := envOr("AISEARCH_GROQ_MODEL", groq.DefaultModel)
	llm := &recordingLLM{LlmService: groq.New(key, groq.DefaultURL, model, os.Getenv("AISEARCH_GROQ_MODE"), 1024)}
	cfg := config.AISearchValues{Model: model, MaxTokens: 1024, LLMDeadlineMillis: deadline}
	s := &svc{rules: staticRules{snap}, llm: llm, cfg: func() config.AISearchValues { return cfg }, now: time.Now}
	t.Logf("model %s, mode %s, deadline %d ms, prompt %d chars + schema %d chars", model, envOr("AISEARCH_GROQ_MODE", groq.ModeJSON), deadline, len(p.system), len(p.schema))

	// ~1.2k tokens a call against the free tier's ~8k tokens/min: space the
	// calls (WH_LIVE_GAP_MS overrides).
	gap := 12 * time.Second
	if ms, err := strconv.Atoi(os.Getenv("WH_LIVE_GAP_MS")); err == nil && ms >= 0 {
		gap = time.Duration(ms) * time.Millisecond
	}
	for i, q := range liveQueries {
		if i > 0 {
			time.Sleep(gap)
		}
		ext, err := s.extract(ctx, p, q, preparse(q))
		usage := "n/a"
		if r := llm.last; r != nil {
			usage = fmt.Sprintf("prompt=%d completion=%d", r.PromptTokens, r.OutputTokens)
		}
		fmt.Printf("\n%q\n  latency=%dms tokens: %s\n", q, llm.latency.Milliseconds(), usage)
		if err != nil {
			fmt.Printf("  FAILED -> basic search (%v)\n", err)
			continue
		}
		b := fromExtraction(snap, ext, locationReq{country: "IN"})
		f := b.filters
		loc := "none"
		if f.Location != nil && (f.Location.Place != "" || f.Location.PostalCode != "") {
			loc = f.Location.Place + f.Location.PostalCode
		}
		area, _ := json.Marshal(f.AreaInput)
		price, _ := json.Marshal(f.PriceInput)
		fmt.Printf("  place=%s radiusKm=%d area=%s price=%s\n  chips=%v industries=%v unmapped=%v notes=%v confidence=%s\n",
			loc, f.RadiusKm, area, price, f.Chips, f.Industries, ext.Unmapped, b.notes, ext.Confidence)
		llm.last = nil
	}
}
