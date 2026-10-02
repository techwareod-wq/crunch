package service

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
)

const (
	// rawWindowDays is how many local days (today included) search_events
	// still holds in full (TTL 90 days, D-106).
	rawWindowDays = 90
	// topKeep is how many top / zero-result queries a day keeps (spec 07).
	topKeep = 200
)

// today is the current local day at midnight.
func (s *svc) today() time.Time {
	n := s.now().In(s.loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, s.loc)
}

// windowStart is the oldest local day whose raw events are all still kept.
func (s *svc) windowStart() time.Time {
	return s.today().AddDate(0, 0, -(rawWindowDays - 1))
}

func dayNum(d time.Time) int64 {
	n, _ := strconv.ParseInt(d.Format("20060102"), 10, 64)
	return n
}

// fromDayNum reads a YYYYMMDD counter as a local day.
func (s *svc) fromDayNum(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102", strconv.FormatInt(n, 10), s.loc)
	return t, err == nil
}

func countryOr(c string) string {
	if c == "" {
		return defaultCountry
	}
	return c
}

// dayAcc accumulates one day + country.
type dayAcc struct {
	row   models.SearchDay
	aiLat []int64
	top   map[string]*models.QueryCount
	zero  map[string]*models.QueryCount
}

type buckets map[string]map[string]*dayAcc // date → country

func (b buckets) acc(date, country string) *dayAcc {
	byCountry := b[date]
	if byCountry == nil {
		byCountry = map[string]*dayAcc{}
		b[date] = byCountry
	}
	a := byCountry[country]
	if a == nil {
		a = &dayAcc{row: models.SearchDay{Date: date, Country: country},
			top: map[string]*models.QueryCount{}, zero: map[string]*models.QueryCount{}}
		byCountry[country] = a
	}
	return a
}

func (a *dayAcc) addEvent(e models.SearchEvent) {
	r := &a.row
	r.Searches++
	zero := e.ResultCount == 0
	if zero {
		r.ZeroResult++
	}
	if e.FallbackUsed {
		r.FallbackUsed++
	}
	if e.Radius != nil && e.Radius.Expanded {
		r.Expanded++
	}
	if e.Kind == models.SearchKindAI {
		r.AISearches++
		if e.AI != nil {
			a.aiLat = append(a.aiLat, e.AI.LatencyMs)
			if !e.AI.Parsed {
				r.AIParseFailures++
			}
		}
	}
	if q := e.NormQuery; q != "" {
		t := a.top[q]
		if t == nil {
			t = &models.QueryCount{Q: q}
			a.top[q] = t
		}
		t.N++
		if zero {
			t.Zero++
		}
	}
	if zero && (e.NormQuery != "" || e.PlaceLabel != "") {
		key := e.NormQuery + "\x00" + e.PlaceLabel
		z := a.zero[key]
		if z == nil {
			z = &models.QueryCount{Q: e.NormQuery, Place: e.PlaceLabel}
			a.zero[key] = z
		}
		z.N++
	}
}

// percentile is the nearest-rank percentile p (0–100) of sorted.
func percentile(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return float64(sorted[min(max(rank, 1), len(sorted))-1])
}

// topOf sorts counts most frequent first (ties by query, then place) and
// keeps limit.
func topOf(m map[string]*models.QueryCount, limit int) []models.QueryCount {
	out := make([]models.QueryCount, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b models.QueryCount) int {
		return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(a.Q, b.Q), cmp.Compare(a.Place, b.Place))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a *dayAcc) finish() models.SearchDay {
	r := a.row
	slices.Sort(a.aiLat)
	r.AILatencyP50, r.AILatencyP95 = percentile(a.aiLat, 50), percentile(a.aiLat, 95)
	r.TopQueries, r.ZeroQueries = topOf(a.top, topKeep), topOf(a.zero, topKeep)
	r.ID = models.SearchDayID(r.Date, r.Country)
	return r
}

// collect rolls up [start, end) per local day and country: non-staff
// searches (D-106) and every enquiry created.
func (s *svc) collect(ctx context.Context, start, end time.Time) (map[string][]models.SearchDay, error) {
	b := buckets{}
	err := s.store.EachEvent(ctx, start, end, func(e models.SearchEvent) error {
		if e.IsStaff {
			return nil
		}
		b.acc(e.At.In(s.loc).Format(time.DateOnly), countryOr(e.Country)).addEvent(e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	enquiries, err := s.store.Enquiries(ctx, start, end)
	if err != nil {
		return nil, err
	}
	for _, q := range enquiries {
		a := b.acc(q.CreatedAt.In(s.loc).Format(time.DateOnly), countryOr(q.Country))
		a.row.Enquiries++
		if q.SearchID != nil {
			a.row.EnquiriesFromSearch++
		}
	}
	out := map[string][]models.SearchDay{}
	for date, byCountry := range b {
		rows := make([]models.SearchDay, 0, len(byCountry))
		for _, a := range byCountry {
			rows = append(rows, a.finish())
		}
		slices.SortFunc(rows, func(x, y models.SearchDay) int { return cmp.Compare(x.Country, y.Country) })
		out[date] = rows
	}
	return out, nil
}

// RollupDaily writes every finished local day after the last rolled-up one
// (yesterday on a normal night), starting no earlier than the raw window
// so no day is rolled from half-expired events. Each day is written whole
// (re-runs converge) before the watermark moves.
func (s *svc) RollupDaily(ctx context.Context, _ analyticsService.RollupPayload) error {
	today := s.today()
	first := s.windowStart()
	through, err := s.store.RolledThrough(ctx)
	if err != nil {
		return err
	}
	if t, ok := s.fromDayNum(through); ok && !t.Before(first) {
		first = t.AddDate(0, 0, 1)
	}
	for d := first; d.Before(today); d = d.AddDate(0, 0, 1) {
		next := d.AddDate(0, 0, 1)
		days, err := s.collect(ctx, d, next)
		if err != nil {
			return err
		}
		date := d.Format(time.DateOnly)
		if err := s.store.ReplaceDays(ctx, date, days[date]); err != nil {
			return err
		}
		if err := s.store.SetRolledThrough(ctx, dayNum(d)); err != nil {
			return err
		}
	}
	return nil
}
