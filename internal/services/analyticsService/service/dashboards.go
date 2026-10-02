package service

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService/dto"
)

const (
	// defaultRangeDays is the dashboard range without from / to.
	defaultRangeDays = 30
	// maxRangeDays caps one dashboard range.
	maxRangeDays = 366
)

// span is a parsed dashboard range: local days start..end inclusive.
type span struct {
	start, end time.Time
	country    string
}

func (p span) from() string { return p.start.Format(time.DateOnly) }
func (p span) to() string   { return p.end.Format(time.DateOnly) }

// parseRange reads r in local days; to defaults to today and is capped at
// it, from defaults to 30 days before to.
func (s *svc) parseRange(r analyticsService.Range) (span, error) {
	today := s.today()
	p := span{end: today, country: strings.ToUpper(strings.TrimSpace(r.Country))}
	if r.To != "" {
		t, err := time.ParseInLocation(time.DateOnly, r.To, s.loc)
		if err != nil {
			return p, analyticsService.Invalidf("to must be a date (YYYY-MM-DD)")
		}
		if t.Before(today) {
			p.end = t
		}
	}
	p.start = p.end.AddDate(0, 0, -(defaultRangeDays - 1))
	if r.From != "" {
		t, err := time.ParseInLocation(time.DateOnly, r.From, s.loc)
		if err != nil {
			return p, analyticsService.Invalidf("from must be a date (YYYY-MM-DD)")
		}
		p.start = t
	}
	if p.start.After(p.end) {
		return p, analyticsService.Invalidf("from must not be after to")
	}
	if p.end.Sub(p.start) > maxRangeDays*24*time.Hour {
		return p, analyticsService.Invalidf("the range is at most %d days", maxRangeDays)
	}
	return p, nil
}

// days returns the range's rollups per date. Rolled-up days come from
// search_daily; the days after the watermark (today included) are
// computed live from the raw events, as far back as the raw window goes.
// Older days never rolled up have no data.
func (s *svc) days(ctx context.Context, p span) (map[string][]models.SearchDay, error) {
	stored, err := s.store.Days(ctx, p.from(), p.to())
	if err != nil {
		return nil, err
	}
	out := map[string][]models.SearchDay{}
	for _, d := range stored {
		out[d.Date] = append(out[d.Date], d)
	}
	through, err := s.store.RolledThrough(ctx)
	if err != nil {
		return nil, err
	}
	liveStart := p.start
	if w := s.windowStart(); w.After(liveStart) {
		liveStart = w
	}
	if t, ok := s.fromDayNum(through); ok && !t.Before(liveStart) {
		liveStart = t.AddDate(0, 0, 1)
	}
	if !liveStart.After(p.end) {
		live, err := s.collect(ctx, liveStart, p.end.AddDate(0, 0, 1))
		if err != nil {
			return nil, err
		}
		for date, rows := range live {
			if _, ok := out[date]; !ok {
				out[date] = rows
			}
		}
	}
	if p.country != "" {
		for date, rows := range out {
			out[date] = slices.DeleteFunc(rows, func(d models.SearchDay) bool { return d.Country != p.country })
		}
	}
	return out, nil
}

func pct(n, of int64) float64 {
	if of == 0 {
		return 0
	}
	return math.Round(1000*float64(n)/float64(of)) / 10
}

// merge sums one date's countries; latencies are averaged by AI volume.
func merge(date string, rows []models.SearchDay) dto.DayPoint {
	d := dto.DayPoint{Date: date}
	var p50, p95 float64
	for _, r := range rows {
		d.Searches += r.Searches
		d.AISearches += r.AISearches
		d.ZeroResult += r.ZeroResult
		d.FallbackUsed += r.FallbackUsed
		d.Expanded += r.Expanded
		d.AIParseFailures += r.AIParseFailures
		d.Enquiries += r.Enquiries
		d.EnquiriesFromSearch += r.EnquiriesFromSearch
		p50 += r.AILatencyP50 * float64(r.AISearches)
		p95 += r.AILatencyP95 * float64(r.AISearches)
	}
	if d.AISearches > 0 {
		d.AILatencyP50 = math.Round(p50 / float64(d.AISearches))
		d.AILatencyP95 = math.Round(p95 / float64(d.AISearches))
	}
	return d
}

func (s *svc) Overview(ctx context.Context, r analyticsService.Range) (dto.Overview, error) {
	p, err := s.parseRange(r)
	if err != nil {
		return dto.Overview{}, err
	}
	days, err := s.days(ctx, p)
	if err != nil {
		return dto.Overview{}, err
	}
	out := dto.Overview{From: p.from(), To: p.to(), Daily: []dto.DayPoint{}}
	t := &out.Totals
	var p50, p95 float64
	for d := p.start; !d.After(p.end); d = d.AddDate(0, 0, 1) {
		date := d.Format(time.DateOnly)
		pt := merge(date, days[date])
		out.Daily = append(out.Daily, pt)
		t.Searches += pt.Searches
		t.AISearches += pt.AISearches
		t.ZeroResult += pt.ZeroResult
		t.FallbackUsed += pt.FallbackUsed
		t.Expanded += pt.Expanded
		t.AIParseFailures += pt.AIParseFailures
		t.Enquiries += pt.Enquiries
		t.EnquiriesFromSearch += pt.EnquiriesFromSearch
		p50 += pt.AILatencyP50 * float64(pt.AISearches)
		p95 += pt.AILatencyP95 * float64(pt.AISearches)
	}
	t.ZeroResultPct = pct(t.ZeroResult, t.Searches)
	t.FallbackPct = pct(t.FallbackUsed, t.Searches)
	t.AIParseFailurePct = pct(t.AIParseFailures, t.AISearches)
	if t.AISearches > 0 {
		t.AILatencyP50 = math.Round(p50 / float64(t.AISearches))
		t.AILatencyP95 = math.Round(p95 / float64(t.AISearches))
	}
	return out, nil
}

// queryTotals merges the days' top queries.
func queryTotals(days map[string][]models.SearchDay) map[string]*models.QueryCount {
	m := map[string]*models.QueryCount{}
	for _, rows := range days {
		for _, r := range rows {
			for _, q := range r.TopQueries {
				c := m[q.Q]
				if c == nil {
					c = &models.QueryCount{Q: q.Q}
					m[q.Q] = c
				}
				c.N += q.N
				c.Zero += q.Zero
			}
		}
	}
	return m
}

// conversions joins the range's enquiries to the searches they came from
// (D-105): enquiries per normalized query (non-staff searches still in the
// raw window), and the enquiries themselves.
func (s *svc) conversions(ctx context.Context, p span) ([]models.EnquiryStat, map[string]int64, error) {
	enquiries, err := s.store.Enquiries(ctx, p.start, p.end.AddDate(0, 0, 1))
	if err != nil {
		return nil, nil, err
	}
	if p.country != "" {
		enquiries = slices.DeleteFunc(enquiries, func(e models.EnquiryStat) bool { return countryOr(e.Country) != p.country })
	}
	ids := []primitive.ObjectID{}
	for _, e := range enquiries {
		if e.SearchID != nil {
			ids = append(ids, *e.SearchID)
		}
	}
	refs, err := s.store.EventRefs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	byID := map[primitive.ObjectID]models.SearchEventRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	byQuery := map[string]int64{}
	for _, e := range enquiries {
		if e.SearchID == nil {
			continue
		}
		if ref, ok := byID[*e.SearchID]; ok && !ref.IsStaff && ref.NormQuery != "" {
			byQuery[ref.NormQuery]++
		}
	}
	return enquiries, byQuery, nil
}

func (s *svc) Top(ctx context.Context, r analyticsService.Range, limit int) (dto.TopQueries, error) {
	p, err := s.parseRange(r)
	if err != nil {
		return dto.TopQueries{}, err
	}
	days, err := s.days(ctx, p)
	if err != nil {
		return dto.TopQueries{}, err
	}
	_, byQuery, err := s.conversions(ctx, p)
	if err != nil {
		return dto.TopQueries{}, err
	}
	out := dto.TopQueries{From: p.from(), To: p.to(), Items: []dto.TopQuery{}}
	for _, q := range topOf(queryTotals(days), limit) {
		out.Items = append(out.Items, dto.TopQuery{Q: q.Q, Searches: q.N, ZeroResults: q.Zero, ZeroShare: pct(q.Zero, q.N), Enquiries: byQuery[q.Q]})
	}
	return out, nil
}

func (s *svc) ZeroResults(ctx context.Context, r analyticsService.Range, limit int) (dto.ZeroQueries, error) {
	p, err := s.parseRange(r)
	if err != nil {
		return dto.ZeroQueries{}, err
	}
	days, err := s.days(ctx, p)
	if err != nil {
		return dto.ZeroQueries{}, err
	}
	m := map[string]*models.QueryCount{}
	for _, rows := range days {
		for _, row := range rows {
			for _, z := range row.ZeroQueries {
				key := z.Q + "\x00" + z.Place
				c := m[key]
				if c == nil {
					c = &models.QueryCount{Q: z.Q, Place: z.Place}
					m[key] = c
				}
				c.N += z.N
			}
		}
	}
	out := dto.ZeroQueries{From: p.from(), To: p.to(), Items: []dto.ZeroQuery{}}
	for _, z := range topOf(m, limit) {
		out.Items = append(out.Items, dto.ZeroQuery{Q: z.Q, Place: z.Place, N: z.N})
	}
	return out, nil
}

func (s *svc) Conversion(ctx context.Context, r analyticsService.Range, limit int) (dto.Conversion, error) {
	p, err := s.parseRange(r)
	if err != nil {
		return dto.Conversion{}, err
	}
	days, err := s.days(ctx, p)
	if err != nil {
		return dto.Conversion{}, err
	}
	enquiries, byQuery, err := s.conversions(ctx, p)
	if err != nil {
		return dto.Conversion{}, err
	}
	out := dto.Conversion{From: p.from(), To: p.to(), ByQuery: []dto.QueryConversion{}, ByListing: []dto.ListingConversion{}}
	for _, rows := range days {
		for _, row := range rows {
			out.Searches += row.Searches
			out.SearchesWithResults += row.Searches - row.ZeroResult
		}
	}
	listings := map[primitive.ObjectID]*dto.ListingConversion{}
	for _, e := range enquiries {
		out.Enquiries++
		if e.SearchID != nil {
			out.EnquiriesFromSearch++
		}
		if l := e.Listing; l != nil {
			c := listings[l.WarehouseID]
			if c == nil {
				c = &dto.ListingConversion{WarehouseID: l.WarehouseID.Hex(), ShortID: l.ShortID, Name: l.Name}
				listings[l.WarehouseID] = c
			}
			c.Enquiries++
			if e.SearchID != nil {
				c.FromSearch++
			}
		}
	}
	out.Rate = pct(out.EnquiriesFromSearch, out.Searches)

	totals := queryTotals(days)
	for q, n := range byQuery {
		var searches int64
		if t := totals[q]; t != nil {
			searches = t.N
		}
		out.ByQuery = append(out.ByQuery, dto.QueryConversion{Q: q, Searches: searches, Enquiries: n, Rate: pct(n, searches)})
	}
	slices.SortFunc(out.ByQuery, func(a, b dto.QueryConversion) int {
		return cmp.Or(cmp.Compare(b.Enquiries, a.Enquiries), cmp.Compare(a.Q, b.Q))
	})
	for _, c := range listings {
		out.ByListing = append(out.ByListing, *c)
	}
	slices.SortFunc(out.ByListing, func(a, b dto.ListingConversion) int {
		return cmp.Or(cmp.Compare(b.Enquiries, a.Enquiries), cmp.Compare(a.ShortID, b.ShortID))
	})
	if len(out.ByQuery) > limit {
		out.ByQuery = out.ByQuery[:limit]
	}
	if len(out.ByListing) > limit {
		out.ByListing = out.ByListing[:limit]
	}
	return out, nil
}
