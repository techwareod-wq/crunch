// Package gsc is the Google Search Console analytics source (LLD §4.1): four
// Search Analytics pulls per settled day over the rolling [D−5..D−2] window,
// normalized into site/page/query/page_query facts with article stamping.
//
// Dim/metric key names below are this source's fact-identity contract —
// renaming one changes DimKey and REQUIRES a full-history replay of "gsc".
package gsc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	gscprovider "github.com/atharva-ng/crunch/internal/providers/impl/gsc"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
)

// SourceName is the registry name and every fact's source value.
const SourceName = "gsc"

// Fact grains.
const (
	GrainSite      = "site"
	GrainPage      = "page"
	GrainQuery     = "query"
	GrainPageQuery = "page_query"
)

// Dim keys.
const (
	DimPage      = "page"
	DimQuery     = "query"
	DimArticleID = "article_id"
)

// Metric keys.
const (
	MetClicks      = "clicks"
	MetImpressions = "impressions"
	MetCTR         = "ctr"
	MetPosition    = "position"
)

// ConfigKeyProperty is the Integrations["gsc"].Config key holding the matched
// Search Console property string.
const ConfigKeyProperty = "property"

// ErrNoPropertyMatch: sites.list returned no property matching the entity's
// website URL — the user hasn't added our service account (or added it on a
// different property). The connections endpoint turns this into a 422 with
// "add the SA and re-check" guidance.
var ErrNoPropertyMatch = errors.New("gsc: no Search Console property matches this website")

// maxRawPayloadBytes is the chunking threshold for one analyticsRaw payload:
// 8 MB, half Mongo's 16 MB doc cap (LLD §2.1). Oversized pulls are split into
// "page_query#0", "page_query#1", … — never truncated (truncated JSON is
// un-replayable).
const maxRawPayloadBytes = 8 << 20

// pull is one grain's fetch shape: the API dimensions and the per-day row cap
// resolved from values.
type pull struct {
	name       string
	dimensions []string
	rowCap     int
}

// gscPayload is the raw-doc payload envelope: the source-native result rows
// under a single key (BSON needs a document at the top level, not an array).
type gscPayload struct {
	Rows []dto.GSCRow `bson:"rows"`
}

// Source implements analytics.Source for Search Console.
type Source struct {
	provider interfaces.GSC
	values   config.GSCValues
}

var _ analytics.Source = (*Source)(nil)

// New builds the source. The provider may be disabled (no SA key configured) —
// fetches then fail loudly instead of silently skipping.
func New(provider interfaces.GSC, values config.GSCValues) *Source {
	return &Source{provider: provider, values: values}
}

func (s *Source) Name() string             { return SourceName }
func (s *Source) RequiresConnection() bool { return true }

// Schedule: daily 07:00, rolling [D−5..D−2] re-fetch window, Pacific-Time
// reporting days (GSC days are America/Los_Angeles, not UTC).
func (s *Source) Schedule() analytics.SourceSchedule {
	return analytics.SourceSchedule{
		Cadence:       "daily",
		DefaultAt:     "07:00",
		WindowDays:    4,
		LagDays:       2,
		ReportingZone: "America/Los_Angeles",
		BackfillDays:  s.values.BackfillDays,
	}
}

// MergePolicy: counts sum, position folds impression-weighted, ctr is
// recomputed from the merged counts (LLD §3.1a).
func (s *Source) MergePolicy() map[string]analytics.MergeOp {
	return map[string]analytics.MergeOp{
		MetClicks:      analytics.MergeSum(),
		MetImpressions: analytics.MergeSum(),
		MetPosition:    analytics.MergeWeighted(MetImpressions),
		MetCTR:         analytics.MergeRecompute(MetClicks, MetImpressions),
	}
}

// VerifyConnection matches the entity's website against the properties the
// service account can see. URL-prefix properties are preferred over sc-domain
// (audit #6: a domain property aggregates ALL subdomains, inflating "your
// site's" totals); sc-domain is the last-resort fallback with that accepted
// caveat. Unverified-permission entries are skipped — querying them 403s.
func (s *Source) VerifyConnection(ctx context.Context, entity *models.WebEntity) (map[string]string, error) {
	sites, err := s.provider.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	available := map[string]bool{}
	for _, site := range sites {
		if site.PermissionLevel == "siteUnverifiedUser" {
			continue
		}
		available[strings.ToLower(site.SiteURL)] = true
	}

	host := utils.ExtractDomain(entity.WebsiteUrl)
	if host == "" {
		return nil, fmt.Errorf("gsc: web entity %s has no parseable website url %q", entity.ID.Hex(), entity.WebsiteUrl)
	}
	candidates := []string{
		"https://" + host + "/",
		"https://www." + host + "/",
		"http://" + host + "/",
		"http://www." + host + "/",
		"sc-domain:" + host,
	}
	for _, c := range candidates {
		if available[c] {
			return map[string]string{ConfigKeyProperty: c}, nil
		}
	}
	return nil, ErrNoPropertyMatch
}

// FetchWindow runs the four grain pulls once per date in the window
// (StartDate == EndDate == the day), so the values caps stay exactly per-day
// and each raw doc's (date, pull) identity maps to one API result. A
// permission-denied response wraps analytics.ErrAuthRevoked — the user removed
// our service account; the orchestrator flips the integration to "error"
// without retrying.
func (s *Source) FetchWindow(ctx context.Context, entity *models.WebEntity, window analytics.DateWindow) ([]analytics.RawPull, error) {
	property := ""
	if state, ok := entity.Integration(SourceName); ok {
		property = state.Config[ConfigKeyProperty]
	}
	if property == "" {
		return nil, fmt.Errorf("gsc: web entity %s has no connected property", entity.ID.Hex())
	}

	dates, err := windowDates(window)
	if err != nil {
		return nil, err
	}

	pulls := []pull{
		{name: "site", dimensions: []string{"date"}},
		{name: "page", dimensions: []string{"date", "page"}, rowCap: s.values.MaxPagesPerDay},
		{name: "query", dimensions: []string{"date", "query"}, rowCap: s.values.TopQueriesPerDay},
		{name: "page_query", dimensions: []string{"date", "page", "query"}, rowCap: s.values.TopPageQueryRowsPerDay},
	}

	var out []analytics.RawPull
	for _, date := range dates {
		for _, p := range pulls {
			rows, err := s.provider.QuerySearchAnalytics(ctx, property, dto.GSCQueryRequest{
				StartDate:  date,
				EndDate:    date,
				Dimensions: p.dimensions,
				RowLimit:   p.rowCap,
			})
			if err != nil {
				if gscprovider.IsPermissionDenied(err) {
					return nil, fmt.Errorf("%w: %w", analytics.ErrAuthRevoked, err)
				}
				return nil, fmt.Errorf("gsc: pull %s for %s: %w", p.name, date, err)
			}
			// Rows arrive sorted by clicks desc, so hitting the cap means the
			// tail was dropped — logged, never silent (requirements edge-case
			// rule). The true dropped count is unknowable without an uncapped
			// query; "at least the cap was reached" is what the API tells us.
			if p.rowCap > 0 && len(rows) >= p.rowCap {
				log.Warn("gsc: per-day cap reached, tail rows dropped",
					"webEntityId", entity.ID.Hex(), "pull", p.name, "date", date, "cap", p.rowCap)
			}
			chunks, err := buildPulls(date, p.name, rows)
			if err != nil {
				return nil, err
			}
			out = append(out, chunks...)
		}
	}
	return out, nil
}

// Normalize maps one raw pull's rows to facts (pure — no DB, no API; replay
// depends on it). Page URLs are canonicalized with the shared normalizer and
// stamped with the entity's article map; the fact date is the raw doc's date
// (fetches are single-day, and replay's delete-then-rebuild ranges must align
// with raw-doc identity).
func (s *Source) Normalize(ctx context.Context, raw *models.AnalyticsRaw, nctx analytics.NormalizeContext) ([]models.AnalyticsFact, error) {
	var payload gscPayload
	if err := bson.Unmarshal(raw.Payload, &payload); err != nil {
		return nil, fmt.Errorf("gsc: unmarshal raw payload %s/%s: %w", raw.Date, raw.Pull, err)
	}

	base := basePull(raw.Pull)
	facts := make([]models.AnalyticsFact, 0, len(payload.Rows))
	for _, row := range payload.Rows {
		var dims map[string]string
		var grain string
		switch base {
		case "site":
			grain = GrainSite
		case "page":
			grain = GrainPage
			page := pageDim(row.Keys, 1)
			if page == "" {
				continue // unparseable page URL — nothing to key the fact on
			}
			dims = map[string]string{DimPage: page}
		case "query":
			grain = GrainQuery
			if len(row.Keys) < 2 {
				continue
			}
			dims = map[string]string{DimQuery: row.Keys[1]}
		case "page_query":
			grain = GrainPageQuery
			page := pageDim(row.Keys, 1)
			if page == "" || len(row.Keys) < 3 {
				continue
			}
			dims = map[string]string{DimPage: page, DimQuery: row.Keys[2]}
		default:
			return nil, fmt.Errorf("gsc: unknown pull %q", raw.Pull)
		}

		if page, ok := dims[DimPage]; ok {
			if articleID, hit := nctx.ArticleURLIndex[page]; hit {
				dims[DimArticleID] = articleID.Hex()
			} else if articleID, hit := nctx.ArticleSlugIndex[lastPathSegment(page)]; hit {
				// Slug fallback: the exact key can't know the public blog
				// path prefix (/blog/<slug>), the last segment can.
				dims[DimArticleID] = articleID.Hex()
			}
		}

		facts = append(facts, models.AnalyticsFact{
			Grain: grain,
			Date:  raw.Date,
			Dims:  dims,
			Metrics: map[string]float64{
				MetClicks:      row.Clicks,
				MetImpressions: row.Impressions,
				MetCTR:         row.CTR,
				MetPosition:    row.Position,
			},
		})
	}
	return facts, nil
}

// lastPathSegment returns the final path segment of a normalized page URL,
// lowercased — the slug-fallback join key. "" for the root or a host-only URL.
func lastPathSegment(page string) string {
	rest := page
	if i := strings.Index(rest, "://"); i != -1 {
		rest = rest[i+3:]
	}
	slash := strings.IndexByte(rest, '/')
	if slash == -1 {
		return ""
	}
	path := strings.TrimRight(rest[slash:], "/")
	if i := strings.LastIndexByte(path, '/'); i != -1 {
		return strings.ToLower(path[i+1:])
	}
	return ""
}

// pageDim extracts and canonicalizes the page URL at keys[idx]; "" when the
// key is missing or not a parseable http(s) URL.
func pageDim(keys []string, idx int) string {
	if len(keys) <= idx {
		return ""
	}
	return utils.NormalizeGSCPageURL(keys[idx])
}

// basePull strips a chunk suffix: "page_query#1" → "page_query".
func basePull(p string) string {
	if i := strings.IndexByte(p, '#'); i != -1 {
		return p[:i]
	}
	return p
}

// buildPulls marshals rows into one RawPull, splitting into "#N"-suffixed
// chunks while any chunk exceeds the payload threshold. Each chunk is a fully
// valid, independently normalizable payload subset.
func buildPulls(date, pullName string, rows []dto.GSCRow) ([]analytics.RawPull, error) {
	chunks := [][]dto.GSCRow{rows}
	for {
		resplit := false
		next := make([][]dto.GSCRow, 0, len(chunks))
		for _, chunk := range chunks {
			payload, err := bson.Marshal(gscPayload{Rows: chunk})
			if err != nil {
				return nil, fmt.Errorf("gsc: marshal %s payload for %s: %w", pullName, date, err)
			}
			if len(payload) > maxRawPayloadBytes && len(chunk) > 1 {
				mid := len(chunk) / 2
				next = append(next, chunk[:mid], chunk[mid:])
				resplit = true
			} else {
				next = append(next, chunk)
			}
		}
		chunks = next
		if !resplit {
			break
		}
	}

	out := make([]analytics.RawPull, 0, len(chunks))
	for i, chunk := range chunks {
		payload, err := bson.Marshal(gscPayload{Rows: chunk})
		if err != nil {
			return nil, fmt.Errorf("gsc: marshal %s payload for %s: %w", pullName, date, err)
		}
		name := pullName
		if len(chunks) > 1 {
			name = fmt.Sprintf("%s#%d", pullName, i)
		}
		out = append(out, analytics.RawPull{Date: date, Pull: name, Payload: payload, RowCount: len(chunk)})
	}
	return out, nil
}

// windowDates expands an inclusive [From, To] window into its date strings.
func windowDates(w analytics.DateWindow) ([]string, error) {
	from, err := time.Parse(analytics.DateLayout, w.From)
	if err != nil {
		return nil, fmt.Errorf("gsc: bad window from %q: %w", w.From, err)
	}
	to, err := time.Parse(analytics.DateLayout, w.To)
	if err != nil {
		return nil, fmt.Errorf("gsc: bad window to %q: %w", w.To, err)
	}
	var dates []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(analytics.DateLayout))
	}
	return dates, nil
}
