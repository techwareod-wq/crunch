package service

import (
	"context"
	"errors"
	"html"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService/store"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

const systemUserID = "system"

var _ aiSearchService.AISearchService = (*svc)(nil)

type svc struct {
	store    store.Store
	rules    domain.Rules
	search   searchService.SearchService
	llm      interfaces.LlmService
	embedder interfaces.Embedder
	dispatch func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error
	cfg      func() config.AISearchValues
	now      func() time.Time

	promptMu sync.Mutex
	prompt   *prompt

	logMu  sync.RWMutex
	logger domain.SearchLogger
}

// NewService builds AI search. llm is the search-dedicated, single-attempt
// Anthropic client (not the token-tracked queue client, spec 05); llm and
// embedder may be nil (no key): searches then run the basic parse and skip
// the semantic fallback.
func NewService(st store.Store, rules domain.Rules, search searchService.SearchService, llm interfaces.LlmService,
	embedder interfaces.Embedder, dispatcher interfaces.Dispatcher, values config.AISearchValues) aiSearchService.AISearchService {
	dispatch := func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error {
		if dispatcher == nil {
			return aiSearchService.ErrNoDispatcher
		}
		return dispatcher.DispatchKeyed(ctx, string(pt), systemUserID, key, payload)
	}
	return &svc{store: st, rules: rules, search: search, llm: llm, embedder: embedder, dispatch: dispatch,
		cfg: func() config.AISearchValues { return values }, now: time.Now}
}

func (s *svc) SetLogger(l domain.SearchLogger) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logger = l
}

func millis(v, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Millisecond
}

// Search runs spec 05's pipeline. Only an empty query errors.
func (s *svc) Search(ctx context.Context, req aiSearchService.Request, v searchService.Viewer) (aiSearchService.Response, error) {
	start := s.now()
	if v.SessionID == "" {
		v.SessionID = strings.TrimSpace(req.SessionID)
	}
	cfg := s.cfg()
	q := normalizeQuery(req.Q, cfg.MaxQueryChars)
	if q == "" {
		return aiSearchService.Response{}, aiSearchService.Invalidf("q is required")
	}
	if req.Page < 0 {
		return aiSearchService.Response{}, aiSearchService.Invalidf("page must be positive")
	}
	loc := locationReq{country: "IN"}
	if l := req.Location; l != nil {
		if c := strings.ToUpper(strings.TrimSpace(l.Country)); c != "" {
			loc.country = c
		}
		loc.point = l.Point
	}

	snap := s.rules.Snapshot()
	h := preparse(q)
	if h.Pincode != "" {
		// Warm the geocode cache while the model runs (spec 05 step 3).
		go func() {
			gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_, _ = s.search.Resolve(gctx, h.Pincode, loc.country)
		}()
	}

	ai := domain.SearchAI{Model: cfg.Model, Notes: []string{}}
	var b built
	ext, err := s.extract(ctx, s.promptFor(snap, loc.country), q, h)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			ai.Notes = append(ai.Notes, "llm_timeout")
		} else if !errors.Is(err, aiSearchService.ErrNoLLM) {
			log.Warn("aisearch: extraction failed", "error", err)
		}
		b = fromHints(snap, q, h, loc)
	} else {
		ai.Parsed = true
		b = fromExtraction(snap, ext, loc)
	}
	ai.Notes = append(ai.Notes, b.notes...)
	b.filters.Text = q
	b.filters.Page = req.Page

	quiet := v
	quiet.Quiet = true
	resp, err := s.search.Search(ctx, b.filters, quiet)
	var ve *searchService.ValidationError
	if errors.As(err, &ve) {
		// A filter the model shaped badly: keep the location and the text.
		ai.Notes = append(ai.Notes, "filters_dropped:"+ve.Msg)
		resp, err = s.search.Search(ctx, domain.SearchFilters{Location: b.filters.Location, Text: q, Page: req.Page}, quiet)
	}
	if err != nil {
		return aiSearchService.Response{}, err
	}

	reason := ""
	switch {
	case !ai.Parsed:
		reason = domain.FallbackLLMFailed
	case !b.mapped:
		reason = domain.FallbackNothingMapped
	case resp.Total < int64(max(cfg.FallbackThreshold, 1)):
		reason = domain.FallbackFewResults
	}
	if reason != "" && resp.Page <= 1 {
		resp.Fallback = s.fallback(ctx, q, loc.country, &resp, reason)
	}
	ai.LatencyMs = s.now().Sub(start).Milliseconds()
	s.logSearch(resp, ai, v)
	return aiSearchService.Response{SearchResponse: resp, AI: ai}, nil
}

// normalizeQuery undoes the request deserializer's HTML escaping ("&amp;"),
// trims, collapses whitespace and caps the length (runes).
func normalizeQuery(q string, maxChars int) string {
	q = strings.Join(strings.Fields(html.UnescapeString(q)), " ")
	if maxChars <= 0 {
		maxChars = 300
	}
	if utf8.RuneCountInString(q) > maxChars {
		q = string([]rune(q)[:maxChars])
	}
	return q
}

// promptFor returns the vocabulary prompt for the snapshot's rulesVersion,
// rebuilding it when the rules changed.
func (s *svc) promptFor(snap *domain.Snapshot, country string) prompt {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if s.prompt == nil || s.prompt.version != snap.Version {
		cat, _ := s.search.Catalog(country)
		p := buildPrompt(snap, cat)
		s.prompt = &p
	}
	return *s.prompt
}

// fallback adds similar matches (D-084): vector search first, the keyword
// $text search when vectors are unavailable or found nothing. Never fails:
// an outage only marks the response degraded.
func (s *svc) fallback(ctx context.Context, q, country string, resp *domain.SearchResponse, reason string) *domain.SearchFallback {
	cfg := s.cfg()
	fq := searchService.FallbackQuery{Country: country, Near: resp.Applied.ResolvedPoint, Limit: max(cfg.FallbackLimit, 1),
		VectorIndex: cfg.VectorIndex, NumCandidates: cfg.VectorNumCandidates}
	for _, c := range resp.Results {
		fq.Exclude = append(fq.Exclude, c.ShortID)
	}
	if cards, err := s.similar(ctx, q, fq); err != nil {
		log.Warn("aisearch: semantic fallback unavailable", "error", err)
		resp.Degraded = append(resp.Degraded, domain.DegradedSemantic)
	} else if len(cards) > 0 {
		return &domain.SearchFallback{Used: true, Reason: reason, Source: domain.FallbackSemantic, Results: cards}
	}
	cards, err := s.search.Keyword(ctx, keywordText(q), fq)
	if err != nil {
		log.Warn("aisearch: keyword fallback failed", "error", err)
		return nil
	}
	if len(cards) == 0 {
		return nil
	}
	return &domain.SearchFallback{Used: true, Reason: reason, Source: domain.FallbackKeyword, Results: cards}
}

func (s *svc) similar(ctx context.Context, q string, fq searchService.FallbackQuery) ([]domain.SearchCard, error) {
	if s.embedder == nil {
		return nil, aiSearchService.ErrNoEmbedder
	}
	vecs, err := s.embedder.Embed(ctx, []string{q}, interfaces.EmbedQuery)
	if err != nil {
		return nil, err
	}
	return s.search.Similar(ctx, vecs[0], fq)
}

func (s *svc) logSearch(resp domain.SearchResponse, ai domain.SearchAI, v searchService.Viewer) {
	s.logMu.RLock()
	l := s.logger
	s.logMu.RUnlock()
	if l == nil || resp.Page > 1 {
		return
	}
	e := domain.SearchEvent{
		SearchID: resp.SearchID, At: s.now().UTC(), Source: "ai", Filters: resp.Applied.Filters,
		ResolvedPoint: resp.Applied.ResolvedPoint, GeocodeSource: resp.Applied.GeocodeSource, Radius: resp.Radius,
		Total: resp.Total, Page: resp.Page, LatencyMs: ai.LatencyMs, Degraded: resp.Degraded,
		UserID: v.UserID, SessionID: v.SessionID, Staff: v.Staff, AI: &ai,
	}
	if resp.Fallback != nil {
		e.Fallback = resp.Fallback.Reason
		e.FallbackCount = len(resp.Fallback.Results)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("aisearch: logger panicked", "panic", r)
			}
		}()
		l.Log(context.Background(), e)
	}()
}
