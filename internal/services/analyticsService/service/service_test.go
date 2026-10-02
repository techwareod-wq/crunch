package service

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

// memStore is an in-memory Store.
type memStore struct {
	mu        sync.Mutex
	events    []models.SearchEvent
	enquiries []models.EnquiryStat
	days      map[string]models.SearchDay
	through   int64
	replaced  []string
}

func newMemStore() *memStore { return &memStore{days: map[string]models.SearchDay{}} }

func (m *memStore) InsertEvent(_ context.Context, e *models.SearchEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *e)
	return nil
}

func (m *memStore) EachEvent(_ context.Context, from, to time.Time, fn func(models.SearchEvent) error) error {
	evs := append([]models.SearchEvent(nil), m.events...)
	sort.Slice(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
	for _, e := range evs {
		if !e.At.Before(from) && e.At.Before(to) {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memStore) EventRefs(_ context.Context, ids []primitive.ObjectID) ([]models.SearchEventRef, error) {
	out := []models.SearchEventRef{}
	for _, e := range m.events {
		for _, id := range ids {
			if e.ID == id {
				out = append(out, models.SearchEventRef{ID: e.ID, NormQuery: e.NormQuery, PlaceLabel: e.PlaceLabel, IsStaff: e.IsStaff})
			}
		}
	}
	return out, nil
}

func (m *memStore) ListEvents(_ context.Context, f models.SearchEventFilter, _, _ int) ([]models.SearchEvent, int64, error) {
	out := []models.SearchEvent{}
	for _, e := range m.events {
		if f.Zero && e.ResultCount != 0 {
			continue
		}
		out = append(out, e)
	}
	return out, int64(len(out)), nil
}

func (m *memStore) UnsetUser(_ context.Context, userID primitive.ObjectID) (int64, error) {
	var n int64
	for i, e := range m.events {
		if e.UserID != nil && *e.UserID == userID {
			m.events[i].UserID = nil
			n++
		}
	}
	return n, nil
}

func (m *memStore) Enquiries(_ context.Context, from, to time.Time) ([]models.EnquiryStat, error) {
	out := []models.EnquiryStat{}
	for _, e := range m.enquiries {
		if !e.CreatedAt.Before(from) && e.CreatedAt.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) Days(_ context.Context, from, to string) ([]models.SearchDay, error) {
	out := []models.SearchDay{}
	for _, d := range m.days {
		if d.Date >= from && d.Date <= to {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memStore) ReplaceDays(_ context.Context, date string, rows []models.SearchDay) error {
	for id, d := range m.days {
		if d.Date == date {
			delete(m.days, id)
		}
	}
	for _, r := range rows {
		m.days[models.SearchDayID(r.Date, r.Country)] = r
	}
	m.replaced = append(m.replaced, date)
	return nil
}

func (m *memStore) RolledThrough(context.Context) (int64, error) { return m.through, nil }

func (m *memStore) SetRolledThrough(_ context.Context, day int64) error {
	m.through = max(m.through, day)
	return nil
}

// --- fixtures ---

var ist = time.FixedZone("IST", 19800)

// day is the fixture day; "now" is the next morning.
var day = time.Date(2026, 9, 30, 0, 0, 0, 0, ist)

func newSvc(st *memStore) *svc {
	now := day.AddDate(0, 0, 1).Add(9 * time.Hour)
	return &svc{store: st, loc: ist, now: func() time.Time { return now }}
}

func at(h int) time.Time { return day.Add(time.Duration(h) * time.Hour).UTC() }

type evOpt func(*models.SearchEvent)

func event(h int, q string, results int64, opts ...evOpt) models.SearchEvent {
	e := models.SearchEvent{ID: primitive.NewObjectID(), At: at(h), Kind: models.SearchKindStructured, NormQuery: q,
		ResultCount: results, Country: "IN", Page: 1}
	for _, o := range opts {
		o(&e)
	}
	return e
}

func aiEv(parsed bool, latency int64) evOpt {
	return func(e *models.SearchEvent) {
		e.Kind = models.SearchKindAI
		e.AI = &models.SearchEventAI{Parsed: parsed, LatencyMs: latency}
	}
}
func staffEv(e *models.SearchEvent)  { e.IsStaff = true }
func expanded(e *models.SearchEvent) { e.Radius = &models.SearchEventRadius{Expanded: true} }
func fallback(e *models.SearchEvent) { e.FallbackUsed = true }
func place(p string) evOpt           { return func(e *models.SearchEvent) { e.PlaceLabel = p } }
func country(c string) evOpt         { return func(e *models.SearchEvent) { e.Country = c } }
func ptr[T any](v T) *T              { return &v }
func enquiry(h int) models.EnquiryStat {
	return models.EnquiryStat{ID: primitive.NewObjectID(), CreatedAt: at(h), Country: "IN"}
}

func fixtureStore() *memStore {
	st := newMemStore()
	st.events = []models.SearchEvent{
		event(1, "cold storage pune", 4, aiEv(true, 900)),
		event(2, "cold storage pune", 0, aiEv(true, 1100), fallback, place("Pune")),
		event(3, "pharma bhiwandi", 0, aiEv(false, 2300), place("Bhiwandi")),
		event(4, "", 0, place("411001")),
		event(5, "", 7, expanded),
		event(6, "cold storage pune", 0, staffEv), // staff: excluded
		event(7, "dubai", 2, country("AE")),
		// the next day: not in the fixture day
		event(25, "tomorrow", 1),
	}
	withSearch := enquiry(8)
	withSearch.SearchID = &st.events[0].ID
	st.enquiries = []models.EnquiryStat{enquiry(9), withSearch}
	return st
}

// --- tests ---

func TestRollupMath(t *testing.T) {
	st := fixtureStore()
	s := newSvc(st)
	if err := s.RollupDaily(ctx, analyticsService.RollupPayload{}); err != nil {
		t.Fatal(err)
	}
	if st.through != 20260930 {
		t.Errorf("watermark = %d, want 20260930", st.through)
	}
	// First run starts at the raw window: 89 finished days.
	if len(st.replaced) != rawWindowDays-1 || st.replaced[len(st.replaced)-1] != "2026-09-30" {
		t.Errorf("rolled %d days, last %v", len(st.replaced), st.replaced[len(st.replaced)-1:])
	}

	in := st.days["2026-09-30|IN"]
	want := models.SearchDay{Searches: 5, AISearches: 3, ZeroResult: 3, FallbackUsed: 1, Expanded: 1, AIParseFailures: 1,
		AILatencyP50: 1100, AILatencyP95: 2300, Enquiries: 2, EnquiriesFromSearch: 1}
	if in.Searches != want.Searches || in.AISearches != want.AISearches || in.ZeroResult != want.ZeroResult ||
		in.FallbackUsed != want.FallbackUsed || in.Expanded != want.Expanded || in.AIParseFailures != want.AIParseFailures ||
		in.AILatencyP50 != want.AILatencyP50 || in.AILatencyP95 != want.AILatencyP95 ||
		in.Enquiries != want.Enquiries || in.EnquiriesFromSearch != want.EnquiriesFromSearch {
		t.Errorf("IN day = %+v", in)
	}
	if len(in.TopQueries) != 2 || in.TopQueries[0] != (models.QueryCount{Q: "cold storage pune", N: 2, Zero: 1}) {
		t.Errorf("top = %+v", in.TopQueries)
	}
	wantZero := []models.QueryCount{{Q: "", Place: "411001", N: 1}, {Q: "cold storage pune", Place: "Pune", N: 1}, {Q: "pharma bhiwandi", Place: "Bhiwandi", N: 1}}
	if len(in.ZeroQueries) != 3 || in.ZeroQueries[0] != wantZero[0] || in.ZeroQueries[1] != wantZero[1] || in.ZeroQueries[2] != wantZero[2] {
		t.Errorf("zero = %+v", in.ZeroQueries)
	}
	if ae := st.days["2026-09-30|AE"]; ae.Searches != 1 {
		t.Errorf("AE day = %+v", ae)
	}

	// A second run the same night has nothing left to do.
	st.replaced = nil
	if err := s.RollupDaily(ctx, analyticsService.RollupPayload{}); err != nil {
		t.Fatal(err)
	}
	if len(st.replaced) != 0 {
		t.Errorf("re-run rolled %v", st.replaced)
	}
}

func TestStaffExcluded(t *testing.T) {
	st := newMemStore()
	st.events = []models.SearchEvent{event(1, "cold", 0, staffEv), event(2, "cold", 0, staffEv, aiEv(false, 10))}
	s := newSvc(st)
	ov, err := s.Overview(ctx, analyticsService.Range{From: "2026-09-30", To: "2026-09-30"})
	if err != nil {
		t.Fatal(err)
	}
	if ov.Totals.Searches != 0 || ov.Totals.AISearches != 0 || ov.Totals.ZeroResult != 0 {
		t.Errorf("staff counted: %+v", ov.Totals)
	}
	top, _ := s.Top(ctx, analyticsService.Range{From: "2026-09-30", To: "2026-09-30"}, 10)
	if len(top.Items) != 0 {
		t.Errorf("staff queries listed: %+v", top.Items)
	}
}

func TestOverviewLiveAndRolledAgree(t *testing.T) {
	r := analyticsService.Range{From: "2026-09-29", To: "2026-10-01"}
	live, err := newSvc(fixtureStore()).Overview(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	st := fixtureStore()
	s := newSvc(st)
	if err := s.RollupDaily(ctx, analyticsService.RollupPayload{}); err != nil {
		t.Fatal(err)
	}
	rolled, err := s.Overview(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if live.Totals != rolled.Totals {
		t.Errorf("live %+v\nrolled %+v", live.Totals, rolled.Totals)
	}
	// 6 non-staff searches (5 IN + 1 AE) on the 30th, 1 on the 1st (today, live).
	if live.Totals.Searches != 7 || len(live.Daily) != 3 || live.Daily[1].Searches != 6 || live.Daily[2].Searches != 1 {
		t.Errorf("overview = %+v", live)
	}
	if live.Totals.ZeroResultPct != 42.9 {
		t.Errorf("zero %% = %v, want 42.9", live.Totals.ZeroResultPct)
	}

	in, _ := s.Overview(ctx, analyticsService.Range{From: "2026-09-30", To: "2026-09-30", Country: "ae"})
	if in.Totals.Searches != 1 {
		t.Errorf("country filter: %+v", in.Totals)
	}
}

func TestConversionJoin(t *testing.T) {
	st := fixtureStore()
	staffSearch := st.events[5].ID
	fromStaff := enquiry(10)
	fromStaff.SearchID = &staffSearch
	listing := &models.EnquiryListing{WarehouseID: primitive.NewObjectID(), ShortID: "abcd2345", Name: "Bhiwandi A"}
	fromStaff.Listing = listing
	plain := enquiry(11)
	plain.Listing = listing
	expired := enquiry(12)
	expired.SearchID = ptr(primitive.NewObjectID()) // raw search gone
	st.enquiries = append(st.enquiries, fromStaff, plain, expired)
	s := newSvc(st)

	c, err := s.Conversion(ctx, analyticsService.Range{From: "2026-09-30", To: "2026-09-30"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if c.Searches != 6 || c.SearchesWithResults != 3 || c.Enquiries != 5 || c.EnquiriesFromSearch != 3 || c.Rate != 50 {
		t.Errorf("funnel = %+v", c)
	}
	// Only the non-staff, still-kept search maps to a query.
	if len(c.ByQuery) != 1 || c.ByQuery[0] != (dto.QueryConversion{Q: "cold storage pune", Searches: 2, Enquiries: 1, Rate: 50}) {
		t.Errorf("by query = %+v", c.ByQuery)
	}
	if len(c.ByListing) != 1 || c.ByListing[0].Enquiries != 2 || c.ByListing[0].FromSearch != 1 {
		t.Errorf("by listing = %+v", c.ByListing)
	}

	top, _ := s.Top(ctx, analyticsService.Range{From: "2026-09-30", To: "2026-09-30"}, 10)
	if top.Items[0].Q != "cold storage pune" || top.Items[0].Enquiries != 1 || top.Items[0].ZeroShare != 50 {
		t.Errorf("top = %+v", top.Items)
	}
}

func TestRangeValidation(t *testing.T) {
	s := newSvc(newMemStore())
	for _, r := range []analyticsService.Range{
		{From: "30-09-2026"}, {To: "nope"}, {From: "2026-10-01", To: "2026-09-01"}, {From: "2024-01-01", To: "2026-09-30"},
	} {
		if _, err := s.Overview(ctx, r); err == nil {
			t.Errorf("%+v: want an error", r)
		}
	}
	ov, err := s.Overview(ctx, analyticsService.Range{})
	if err != nil || ov.To != "2026-10-01" || ov.From != "2026-09-02" || len(ov.Daily) != 30 {
		t.Errorf("default range = %s..%s (%d days), %v", ov.From, ov.To, len(ov.Daily), err)
	}
}

func TestLogMapsEvent(t *testing.T) {
	st := newMemStore()
	s := newSvc(st)
	id := primitive.NewObjectID()
	uid := primitive.NewObjectID()
	s.Log(ctx, domain.SearchEvent{
		SearchID: id.Hex(), At: at(1), Source: "ai", Total: 0, Page: 1, LatencyMs: 1500,
		Filters: domain.SearchFilters{Text: "Cold storage, 10,000 sq.ft near Pune!", Location: &domain.SearchLocation{Place: "Pune", Country: "IN"}, RadiusKm: 25},
		Radius:  &domain.SearchRadius{RequestedKm: 25, UsedKm: 50, Expanded: true},
		UserID:  uid.Hex(), SessionID: "s1", AI: &domain.SearchAI{Parsed: true, Model: "m", LatencyMs: 1400},
		Fallback: domain.FallbackFewResults, FallbackCount: 3,
		Degraded:      nil,
		Staff:         false,
		GeocodeSource: domain.GeoSourceCache,
	})
	s.Log(ctx, domain.SearchEvent{SearchID: "not-an-id"})
	if len(st.events) != 1 {
		t.Fatalf("events = %d, want 1", len(st.events))
	}
	e := st.events[0]
	if e.ID != id || e.Kind != models.SearchKindAI || e.NormQuery != "cold storage 10000 sq ft near pune" || e.PlaceLabel != "Pune" ||
		e.Country != "IN" || e.UserID == nil || *e.UserID != uid || e.SessionID != "s1" || !e.FallbackUsed || e.FallbackCount != 3 ||
		e.Radius == nil || !e.Radius.Expanded || e.AI == nil || e.AI.LatencyMs != 1400 || e.Degraded == nil {
		t.Errorf("event = %+v", e)
	}
	if e.Filters["radiusKm"] != float64(25) || e.Filters["text"] == nil {
		t.Errorf("filters = %v", e.Filters)
	}

	n, _ := s.Cleaner().DeleteUserData(ctx, uid)
	if n != 1 || st.events[0].UserID != nil {
		t.Errorf("cleaner: n=%d user=%v", n, st.events[0].UserID)
	}
}

func TestNormalizeQuery(t *testing.T) {
	for in, want := range map[string]string{
		"  Cold   STORAGE ":    "cold storage",
		"10,000 sq ft":         "10000 sq ft",
		"₹1.5 lakh/month":      "1.5 lakh month",
		"pharma, bhiwandi.":    "pharma bhiwandi",
		"400703":               "400703",
		"FMCG—Navi Mumbai (W)": "fmcg navi mumbai w",
	} {
		if got := NormalizeQuery(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
