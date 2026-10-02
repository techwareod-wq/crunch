package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var _ analyticsService.AnalyticsService = (*svc)(nil)

const (
	// logTimeout bounds one event write; the search is long answered.
	logTimeout = 2 * time.Second
	// maxNormQuery caps the normalized query (runes).
	maxNormQuery = 200
	// defaultCountry is the country of a search without a location
	// (India-only launch, D-003).
	defaultCountry = "IN"
)

type svc struct {
	store store.Store
	loc   *time.Location
	now   func() time.Time
}

// NewService builds the analytics service. Calendar days follow
// warehousehub.analytics.zone (UTC when it doesn't load).
func NewService(st store.Store, v config.AnalyticsValues) analyticsService.AnalyticsService {
	loc, err := time.LoadLocation(v.Zone)
	if err != nil || v.Zone == "" {
		log.Warn("analytics: zone not loaded, using UTC", "zone", v.Zone, "error", err)
		loc = time.UTC
	}
	return &svc{store: st, loc: loc, now: time.Now}
}

// --- writer (domain.SearchLogger) ---

// Log writes one search event. Never fails the search: errors are logged.
func (s *svc) Log(ctx context.Context, e domain.SearchEvent) {
	ev, ok := toEvent(e, s.now().UTC())
	if !ok {
		log.Warn("analytics: search event without a valid searchId", "searchId", e.SearchID)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, logTimeout)
	defer cancel()
	if err := s.store.InsertEvent(ctx, ev); err != nil {
		log.Warn("analytics: search event write failed", "searchId", e.SearchID, "error", err)
	}
}

// toEvent maps a search onto its search_events doc.
func toEvent(e domain.SearchEvent, now time.Time) (*models.SearchEvent, bool) {
	id, err := primitive.ObjectIDFromHex(e.SearchID)
	if err != nil {
		return nil, false
	}
	ev := &models.SearchEvent{
		ID: id, At: e.At.UTC(), SessionID: e.SessionID, IsStaff: e.Staff, Kind: models.SearchKindStructured,
		RawText: e.Filters.Text, NormQuery: NormalizeQuery(e.Filters.Text), Filters: filtersDoc(e.Filters),
		GeocodeSource: e.GeocodeSource, ResultCount: e.Total, FallbackUsed: e.Fallback != "",
		FallbackReason: e.Fallback, FallbackCount: e.FallbackCount, LatencyMs: e.LatencyMs,
		Page: max(e.Page, 1), Country: defaultCountry, Degraded: e.Degraded,
	}
	if ev.At.IsZero() {
		ev.At = now
	}
	if ev.Degraded == nil {
		ev.Degraded = []string{}
	}
	if e.Source == "ai" {
		ev.Kind = models.SearchKindAI
	}
	if uid, err := primitive.ObjectIDFromHex(e.UserID); err == nil {
		ev.UserID = &uid
	}
	if l := e.Filters.Location; l != nil {
		if l.Country != "" {
			ev.Country = l.Country
		}
		ev.PlaceLabel = l.Place
		if ev.PlaceLabel == "" {
			ev.PlaceLabel = l.PostalCode
		}
	}
	if p := e.ResolvedPoint; p != nil {
		ev.ResolvedPoint = &models.SearchEventPoint{Lat: p.Lat, Lng: p.Lng}
	}
	if r := e.Radius; r != nil {
		ev.Radius = &models.SearchEventRadius{RequestedKm: r.RequestedKm, UsedKm: r.UsedKm, Expanded: r.Expanded, Exhausted: r.Exhausted}
	}
	if a := e.AI; a != nil {
		notes := a.Notes
		if notes == nil {
			notes = []string{}
		}
		ev.AI = &models.SearchEventAI{Parsed: a.Parsed, Model: a.Model, LatencyMs: a.LatencyMs, Notes: notes}
	}
	return ev, true
}

// filtersDoc stores the filters with the API's camelCase keys.
func filtersDoc(f domain.SearchFilters) bson.M {
	doc := bson.M{}
	if b, err := json.Marshal(f); err == nil {
		_ = json.Unmarshal(b, &doc)
	}
	return doc
}

// NormalizeQuery is the grouping key of a typed query: lowercased,
// punctuation to spaces, whitespace collapsed, digits kept (a thousands
// comma is dropped and a decimal point kept, so "10,000" and "1.5" stay
// whole).
func NormalizeQuery(q string) string {
	rs := []rune(strings.ToLower(q))
	var b strings.Builder
	for i, r := range rs {
		between := i > 0 && i < len(rs)-1 && unicode.IsDigit(rs[i-1]) && unicode.IsDigit(rs[i+1])
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ',' && between:
		case r == '.' && between:
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > maxNormQuery {
		out = strings.TrimSpace(string(r[:maxNormQuery]))
	}
	return out
}

// --- raw log ---

func (s *svc) Events(ctx context.Context, f models.SearchEventFilter, page, limit int) ([]models.SearchEvent, int64, error) {
	f.Q = NormalizeQuery(f.Q)
	return s.store.ListEvents(ctx, f, page, limit)
}

// --- account deletion (D-019) ---

func (s *svc) Cleaner() accountService.DataCleaner { return cleaner{s} }

type cleaner struct{ s *svc }

func (c cleaner) Name() string { return "searchEvents" }

// DeleteUserData unsets the user id on the user's searches; the anonymous
// events stay for analytics (D-019).
func (c cleaner) DeleteUserData(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return c.s.store.UnsetUser(ctx, userID)
}
