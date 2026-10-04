package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	sdto "github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

// --- fakes ---

type fakeSearch struct {
	mu       sync.Mutex
	filters  []domain.SearchFilters
	viewers  []searchService.Viewer
	total    int64
	results  []domain.SearchCard
	invalid  bool // first Search call returns a ValidationError
	similar  []domain.SearchCard
	simErr   error
	keyword  []domain.SearchCard
	kwText   string
	resolved []string
}

func (f *fakeSearch) Search(_ context.Context, fl domain.SearchFilters, v searchService.Viewer) (domain.SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, fl)
	f.viewers = append(f.viewers, v)
	if f.invalid {
		f.invalid = false
		return domain.SearchResponse{}, searchService.Invalidf("areaSqm: min is above max")
	}
	return domain.SearchResponse{SearchID: "s1", Results: f.results, Total: f.total, Page: max(fl.Page, 1),
		Applied: domain.SearchApplied{Filters: fl}, Degraded: []string{}}, nil
}
func (f *fakeSearch) Map(context.Context, *domain.SearchFilters, string, bool) (sdto.MapResponse, string, error) {
	return sdto.MapResponse{}, "", nil
}
func (f *fakeSearch) Catalog(string, bool) (sdto.PublicCatalog, string) {
	return sdto.PublicCatalog{RulesVersion: 7,
		ChipRows:   []sdto.ChipRow{{Row: "Storage", Chips: []sdto.Chip{{Key: "cold_storage", Label: "Cold storage"}, {Key: "cold_storage.temp_type:frozen", Label: "Frozen"}}}},
		Ranges:     []sdto.RangeFilter{{Key: "cold_storage.temperature", Label: "Cold storage · Temperature", Type: "number", Unit: "C"}},
		Industries: []sdto.IndustryItem{{Key: "pharma", Name: "Pharma"}}}, `"c"`
}
func (f *fakeSearch) Resolve(_ context.Context, q, _ string) (sdto.GeoResolved, error) {
	f.mu.Lock()
	f.resolved = append(f.resolved, q)
	f.mu.Unlock()
	return sdto.GeoResolved{}, nil
}
func (f *fakeSearch) Nearest(context.Context, models.GeoPoint, int, primitive.ObjectID) ([]domain.ListingCard, error) {
	return nil, nil
}
func (f *fakeSearch) Similar(context.Context, []float32, searchService.FallbackQuery) ([]domain.SearchCard, error) {
	return f.similar, f.simErr
}
func (f *fakeSearch) Keyword(_ context.Context, text string, _ searchService.FallbackQuery) ([]domain.SearchCard, error) {
	f.kwText = text
	return f.keyword, nil
}
func (f *fakeSearch) SetLogger(domain.SearchLogger) {}

type fakeLLM struct {
	input string // tool input JSON; "" = no tool call
	err   error
	block bool // wait for the ctx deadline
	stop  string
	reqs  []dto.PromptRequest
}

func (l *fakeLLM) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	l.reqs = append(l.reqs, req)
	if l.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if l.err != nil {
		return nil, l.err
	}
	resp := &dto.PromptResponse{StopReason: l.stop}
	if l.input != "" {
		resp.ToolUse = &dto.ToolUse{Name: toolName, Input: json.RawMessage(l.input)}
	}
	return resp, nil
}

type fakeEmbedder struct {
	calls int
	err   error
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string, _ string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2}
	}
	return out, nil
}
func (e *fakeEmbedder) Model() string { return "voyage-test" }

type fakeStore struct {
	w      map[primitive.ObjectID]*models.Warehouse
	writes int
	refs   []models.LiveWarehouseRef
}

func (s *fakeStore) GetWarehouse(_ context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	if w, ok := s.w[id]; ok {
		cp := *w
		return &cp, nil
	}
	return nil, models.ErrNotFound
}
func (s *fakeStore) SetEmbedding(_ context.Context, id primitive.ObjectID, v int, vec []float32, hash string) (bool, error) {
	w := s.w[id]
	if w == nil || w.LiveVersion != v {
		return false, nil
	}
	s.writes++
	w.Embedding, w.EmbeddingHash = vec, hash
	return true, nil
}
func (s *fakeStore) LiveRefs(_ context.Context, after primitive.ObjectID, limit int) ([]models.LiveWarehouseRef, error) {
	var out []models.LiveWarehouseRef
	for _, r := range s.refs {
		if r.ID.Hex() > after.Hex() && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

type staticRules struct{ s *domain.Snapshot }

func (r staticRules) Snapshot() *domain.Snapshot { return r.s }
func (r staticRules) SnapshotAtLeast(context.Context, int64) (*domain.Snapshot, error) {
	return r.s, nil
}

func snapshot() *domain.Snapshot {
	cold := models.AttributeNode{Key: "cold_storage", ParentKey: domain.RootKey, Name: "Cold storage", Public: true, Filterable: true,
		Synonyms: []string{"cold room", "thanda"},
		Fields: []models.AttributeField{
			{Key: "temperature", Name: "Temperature", Type: domain.TypeNumber, Public: true, Filterable: true, Unit: &models.UnitSpec{Family: domain.DimTemp}},
			{Key: "temp_type", Name: "Type", Type: domain.TypePick, Public: true, Filterable: true,
				Options: []models.FieldOption{{Key: "frozen", Label: "Frozen"}}},
		}}
	ind := models.Industry{Key: "pharma", Name: "Pharma", Required: []models.Condition{{Node: "cold_storage", Cmp: domain.CmpIsYes}}}
	return domain.NewSnapshot(7, []models.AttributeNode{domain.RootNode(), cold}, []models.Industry{ind})
}

type harness struct {
	search *fakeSearch
	llm    *fakeLLM
	emb    *fakeEmbedder
	store  *fakeStore
	sent   []string
	svc    *svc
}

func newHarness() *harness {
	h := &harness{search: &fakeSearch{total: 10}, llm: &fakeLLM{}, emb: &fakeEmbedder{}, store: &fakeStore{w: map[primitive.ObjectID]*models.Warehouse{}}}
	cfg := config.AISearchValues{Enabled: true, Model: "claude-sonnet-5", MaxTokens: 1024, LLMDeadlineMillis: 50, MaxQueryChars: 300,
		FallbackThreshold: 5, FallbackLimit: 10, VectorIndex: "wh_embedding", VectorNumCandidates: 200}
	h.svc = &svc{store: h.store, rules: staticRules{snapshot()}, search: h.search, llm: h.llm, embedder: h.emb,
		dispatch: func(_ context.Context, _ pipeline.ProcessType, key string, _ any) error {
			h.sent = append(h.sent, key)
			return nil
		},
		cfg: func() config.AISearchValues { return cfg }, now: time.Now}
	return h
}

// --- tests ---

func TestAISearchParsed(t *testing.T) {
	h := newHarness()
	h.llm.input = `{"location":{"kind":"place","text":"Chakan"},"radiusKm":40,
		"area":{"value":10,"unit":"sqft","intent":"approx"},"price":{"amount":20,"basis":"per_sqft_month","intent":"max"},
		"industries":["pharma"],"chips":["cold_storage","ghost"],
		"ranges":[{"key":"cold_storage.temperature","max":0,"min":32,"unit":"F"}],
		"sort":"cheapest","unmapped":["24x7 security"],"confidence":"high"}`
	resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "  pharma cold   storage near Chakan &amp; 24x7 security  ",
		Location: &domain.SearchLocation{Country: "in", Point: &domain.LatLng{Lat: 1, Lng: 2}}}, searchService.Viewer{UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AI.Parsed || resp.Fallback != nil || !slices.Contains(resp.AI.Notes, "unmapped:24x7 security") {
		t.Errorf("ai = %+v fallback = %+v", resp.AI, resp.Fallback)
	}
	f := h.search.filters[0]
	if f.Location.Place != "Chakan" || f.Location.Country != "IN" || f.Location.Point != nil || f.RadiusKm != 40 {
		t.Errorf("location = %+v radius %d", f.Location, f.RadiusKm)
	}
	sqm := 10 * domain.SqmPerSqft
	if math.Abs(*f.AreaSqm.Min-sqm*0.8) > 1e-9 || math.Abs(*f.AreaSqm.Max-sqm*1.2) > 1e-9 {
		t.Errorf("area = %v–%v", *f.AreaSqm.Min, *f.AreaSqm.Max)
	}
	if f.Price.PerSqmMonthMax == nil || math.Abs(*f.Price.PerSqmMonthMax-21527.82) > 0.01 || f.Price.Currency != "INR" {
		t.Errorf("price = %+v", f.Price)
	}
	// °F converted to °C and the bounds put in order; unknown sort dropped.
	r := f.Ranges["cold_storage.temperature"]
	if math.Abs(*r.Min+17.7778) > 1e-3 || math.Abs(*r.Max) > 1e-9 || f.Sort != "" {
		t.Errorf("range = %v..%v sort %q", *r.Min, *r.Max, f.Sort)
	}
	if f.Text != "pharma cold storage near Chakan & 24x7 security" || !h.search.viewers[0].Quiet {
		t.Errorf("text = %q viewer %+v", f.Text, h.search.viewers[0])
	}
	req := h.llm.reqs[0]
	if req.ForceTool != toolName || !req.DisableThinking || req.Temperature != nil || !req.Messages[0].Cache || req.Model != "claude-sonnet-5" {
		t.Errorf("llm request = %+v", req)
	}
	if !strings.Contains(req.Messages[0].Content, "cold_storage: Cold storage (also: cold room, thanda)") {
		t.Errorf("vocabulary missing synonyms:\n%s", req.Messages[0].Content)
	}
	var schema map[string]any
	if err := json.Unmarshal(req.Tools[0].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	chips := schema["properties"].(map[string]any)["chips"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	if len(chips) != 2 {
		t.Errorf("chip enum = %v", chips)
	}
}

func TestAISearchLLMFailuresFallBack(t *testing.T) {
	for name, set := range map[string]func(l *fakeLLM){
		"timeout":    func(l *fakeLLM) { l.block = true },
		"api error":  func(l *fakeLLM) { l.err = errors.New("529 overloaded") },
		"no tool":    func(l *fakeLLM) {},
		"malformed":  func(l *fakeLLM) { l.input = `{"chips": "cold_storage"` },
		"wrong type": func(l *fakeLLM) { l.input = `{"chips": 5}` },
		"truncated":  func(l *fakeLLM) { l.input = `{}`; l.stop = dto.StopReasonMaxTokens },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			set(h.llm)
			h.search.similar = []domain.SearchCard{{ShortID: "sim"}}
			start := time.Now()
			resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "thanda godown 10k sqft near 411001 under ₹30/sqft"}, searchService.Viewer{})
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > time.Second {
				t.Errorf("took %v", time.Since(start))
			}
			if resp.AI.Parsed || !slices.Contains(resp.AI.Notes, "basic_search") {
				t.Errorf("ai = %+v", resp.AI)
			}
			if name == "timeout" && !slices.Contains(resp.AI.Notes, "llm_timeout") {
				t.Errorf("notes = %v", resp.AI.Notes)
			}
			f := h.search.filters[0]
			if f.Location.PostalCode != "411001" || math.Abs(*f.AreaSqm.Min-10000*domain.SqmPerSqft) > 0.01 || f.Price.PerSqmMonthMax == nil ||
				!slices.Equal(f.Chips, []string{"cold_storage"}) {
				t.Errorf("basic filters = %+v", f)
			}
			if fb := resp.Fallback; fb == nil || fb.Reason != domain.FallbackLLMFailed || fb.Source != domain.FallbackSemantic {
				t.Errorf("fallback = %+v", fb)
			}
		})
	}
}

func TestAISearchFallbackChain(t *testing.T) {
	h := newHarness()
	h.llm.input = `{"location":{"kind":"none"},"industries":[],"chips":[],"unmapped":["vibes"],"confidence":"low"}`
	h.search.results = []domain.SearchCard{{ShortID: "a"}}
	h.search.simErr = errors.New("no $vectorSearch")
	h.search.keyword = []domain.SearchCard{{ShortID: "kw"}}
	resp, _ := h.svc.Search(ctx, aiSearchService.Request{Q: "nice spacious warehouse near the highway 500 sqft"}, searchService.Viewer{})
	fb := resp.Fallback
	if fb == nil || fb.Reason != domain.FallbackNothingMapped || fb.Source != domain.FallbackKeyword || fb.Results[0].ShortID != "kw" {
		t.Fatalf("fallback = %+v", fb)
	}
	if !slices.Contains(resp.Degraded, domain.DegradedSemantic) {
		t.Errorf("degraded = %v", resp.Degraded)
	}
	if h.search.kwText != "nice spacious highway" {
		t.Errorf("keyword text = %q", h.search.kwText)
	}

	// Enough results and something mapped: no fallback; page 2 never falls back.
	h = newHarness()
	h.llm.input = `{"location":{"kind":"none"},"industries":[],"chips":["cold_storage"],"unmapped":[],"confidence":"high"}`
	if resp, _ := h.svc.Search(ctx, aiSearchService.Request{Q: "cold storage"}, searchService.Viewer{}); resp.Fallback != nil {
		t.Errorf("unexpected fallback %+v", resp.Fallback)
	}
	h.search.total = 1
	h.search.similar = []domain.SearchCard{{ShortID: "sim"}}
	if resp, _ := h.svc.Search(ctx, aiSearchService.Request{Q: "cold storage", Page: 2}, searchService.Viewer{}); resp.Fallback != nil {
		t.Errorf("page 2 fallback %+v", resp.Fallback)
	}
	if resp, _ := h.svc.Search(ctx, aiSearchService.Request{Q: "cold storage"}, searchService.Viewer{}); resp.Fallback == nil || resp.Fallback.Reason != domain.FallbackFewResults {
		t.Errorf("few results fallback = %+v", resp.Fallback)
	}
}

func TestAISearchRetriesWithoutBadFilters(t *testing.T) {
	h := newHarness()
	h.llm.input = `{"location":{"kind":"pincode","text":"411001"},"industries":[],"chips":["cold_storage"],"unmapped":[],"confidence":"high"}`
	h.search.invalid = true
	resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "cold storage 411001"}, searchService.Viewer{})
	if err != nil || len(h.search.filters) != 2 {
		t.Fatalf("err %v calls %d", err, len(h.search.filters))
	}
	if f := h.search.filters[1]; f.Location.PostalCode != "411001" || len(f.Chips) != 0 || !strings.HasPrefix(resp.AI.Notes[0], "filters_dropped") {
		t.Errorf("retry = %+v notes %v", f, resp.AI.Notes)
	}
}

func TestAISearchValidation(t *testing.T) {
	h := newHarness()
	var ve *aiSearchService.ValidationError
	if _, err := h.svc.Search(ctx, aiSearchService.Request{Q: "   "}, searchService.Viewer{}); !errors.As(err, &ve) {
		t.Errorf("empty q = %v", err)
	}
	long := strings.Repeat("é", 400)
	if got := normalizeQuery(long, 300); len([]rune(got)) != 300 {
		t.Errorf("cap = %d runes", len([]rune(got)))
	}
}

func TestPriceIntents(t *testing.T) {
	var f domain.SearchFilters
	if note := applyPrice(&f, 500000, "", domain.BasisFlatMonth, intentMax); note != "price_needs_area" || f.Price != nil {
		t.Errorf("flat without area: %q %+v", note, f.Price)
	}
	applyArea(&f, 1000, domain.UnitSqm, "")
	if f.AreaInput.Intent != intentMin || *f.AreaSqm.Min != 1000 {
		t.Errorf("bare area = %+v", f.AreaSqm)
	}
	applyPrice(&f, 500000, "inr", domain.BasisFlatMonth, intentApprox)
	if *f.Price.PerSqmMonthMin != 40000 || *f.Price.PerSqmMonthMax != 60000 {
		t.Errorf("flat approx = %v–%v", *f.Price.PerSqmMonthMin, *f.Price.PerSqmMonthMax)
	}
	applyPrice(&f, 400, "", domain.BasisPerSqmMonth, intentMin)
	if *f.Price.PerSqmMonthMin != 40000 || f.Price.PerSqmMonthMax != nil {
		t.Errorf("min = %+v", f.Price)
	}
}

func TestScanChips(t *testing.T) {
	got := scanChips(snapshot(), "Need a THANDA godown with frozen section, cold-room ok")
	if !slices.Equal(got, []string{"cold_storage", "cold_storage.temp_type:frozen"}) {
		t.Errorf("chips = %v", got)
	}
}

func TestEmbedJob(t *testing.T) {
	h := newHarness()
	id := primitive.NewObjectID()
	h.store.w[id] = &models.Warehouse{ID: id, Status: models.WarehouseLive, LiveVersion: 3, Name: "Pune Cold Hub", City: "Pune", Locality: "Chakan", TotalSqm: 929,
		Live: &models.ListingContent{Attributes: models.Attributes{
			domain.RootKey: {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{"description": {V: "Near the highway."}}},
			"cold_storage": {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{"temp_type": {V: "frozen"}}},
		}}}
	p := aiSearchService.EmbedPayload{WarehouseID: id.Hex(), LiveVersion: 3}
	if err := h.svc.Embed(ctx, p); err != nil || h.store.writes != 1 {
		t.Fatalf("err %v writes %d", err, h.store.writes)
	}
	want := "Pune Cold Hub\nNear the highway.\nChakan, Pune\nFeatures: Cold storage, Frozen\nSuits: Pharma\nTotal area: 929 sq m"
	if got := summaryText(snapshot(), h.store.w[id]); got != want {
		t.Errorf("summary =\n%s", got)
	}
	// Unchanged text + model: skipped. Stale version: skipped. Gone: no error.
	h.svc.Embed(ctx, p)
	h.svc.Embed(ctx, aiSearchService.EmbedPayload{WarehouseID: id.Hex(), LiveVersion: 2})
	if err := h.svc.Embed(ctx, aiSearchService.EmbedPayload{WarehouseID: primitive.NewObjectID().Hex(), LiveVersion: 1}); err != nil || h.emb.calls != 1 {
		t.Errorf("err %v embed calls %d", err, h.emb.calls)
	}
	var permanent error = h.svc.Embed(ctx, aiSearchService.EmbedPayload{WarehouseID: "nope"})
	if !errors.Is(permanent, pipeline.ErrPermanent) {
		t.Errorf("bad id = %v", permanent)
	}
	// Feature off: a queued job does nothing.
	off := h.svc.cfg()
	off.Enabled = false
	h.svc.cfg = func() config.AISearchValues { return off }
	h.store.w[id].EmbeddingHash = ""
	if err := h.svc.Embed(ctx, p); err != nil || h.emb.calls != 1 {
		t.Errorf("flag off: err %v calls %d", err, h.emb.calls)
	}
	h.svc.cfg = func() config.AISearchValues { off.Enabled = true; return off }
	// No embedder: a no-op.
	h.svc.embedder = nil
	if err := h.svc.Embed(ctx, p); err != nil {
		t.Error(err)
	}
}

func TestReembedAll(t *testing.T) {
	h := newHarness()
	for i := 0; i < 3; i++ {
		h.store.refs = append(h.store.refs, models.LiveWarehouseRef{ID: primitive.NewObjectID(), LiveVersion: i + 1})
	}
	run, err := h.svc.StartReembedAll(ctx)
	if err != nil || len(h.sent) != 1 || h.sent[0] != "reembed_all:"+run {
		t.Fatalf("start = %v %v", h.sent, err)
	}
	if err := h.svc.ReembedAll(ctx, aiSearchService.ReembedAllPayload{Run: run}); err != nil {
		t.Fatal(err)
	}
	if len(h.sent) != 4 || h.sent[1] != aiSearchService.EmbedKey(h.store.refs[0].ID.Hex(), 1, run) {
		t.Errorf("sent = %v", h.sent)
	}
}

func TestPincodeWarmsGeocode(t *testing.T) {
	h := newHarness()
	h.llm.block = true
	h.svc.Search(ctx, aiSearchService.Request{Q: "warehouse 400703"}, searchService.Viewer{})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.search.mu.Lock()
		n := len(h.search.resolved)
		h.search.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("pincode not resolved in parallel")
}
