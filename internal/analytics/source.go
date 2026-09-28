// Package analytics is the raw→facts analytics engine (ANALYTICS_ENGINE_LLD):
// per-source cron beats enqueue (entity, window) units; the shared ingest
// orchestrator fetches source-native payloads into analyticsRaw, normalizes
// them into analyticsFacts (the only tier read APIs touch), and the admin
// replay re-runs normalization over stored raw without re-fetching. Adding a
// source = one new package under sources/ + one Register call at boot + one
// values entry — no engine, cron, or collection changes.
package analytics

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// ErrAuthRevoked marks a FetchWindow failure meaning our access to the source
// was revoked (GSC: the user removed the service account — a 403-class API
// error). Sources wrap such errors with it; the orchestrator turns it into
// integrations.<source>.status = "error" and completes the unit WITHOUT retry
// (no DLQ noise for a state only the user can fix).
var ErrAuthRevoked = errors.New("analytics: source access revoked")

// DateWindow is an inclusive [From, To] range of source-reported date strings
// ("2006-01-02" — lexicographic order equals date order). A single-date window
// has From == To.
type DateWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RawPull is one fetched payload: the source-native response for one (date,
// pull) — exactly what gets upserted into analyticsRaw. An oversized payload
// must be CHUNKED by the fetcher into "page_query#0", "page_query#1", … (each
// independently normalizable), never truncated.
type RawPull struct {
	Date     string
	Pull     string
	Payload  bson.Raw
	RowCount int
}

// NormalizeContext carries cross-collection lookups a normalizer may need,
// resolved ONCE per ingest run by the orchestrator (not per raw doc).
type NormalizeContext struct {
	// ArticleURLIndex maps publish.normalized_url → scheduledArticle._id for
	// the entity being ingested — the GSC article-stamping join. Replays
	// re-stamp with the CURRENT map, so a healed URL map back-fixes history.
	ArticleURLIndex map[string]primitive.ObjectID
	// ArticleSlugIndex maps published articles' effective slugs (lowercased) →
	// scheduledArticle._id — the stamping FALLBACK when the exact URL misses:
	// headless publishes can't know the public blog path prefix, so a page
	// whose last path segment equals a slug is that article.
	ArticleSlugIndex map[string]primitive.ObjectID
}

// SourceSchedule is a source's default cadence and re-fetch window; the values
// file's cron.jobs.analytics_<source> entry overrides the schedule knobs.
type SourceSchedule struct {
	Cadence    string // "daily" | "monthly"
	DefaultAt  string // "07:00" — wall-clock default for the cron beat
	DayOfMonth int    // monthly only, e.g. 1
	// WindowDays/LagDays define the daily rolling re-fetch window
	// [today−(LagDays+WindowDays−1) … today−LagDays]: GSC fetches [D−5..D−2]
	// ⇒ lag 2, span 4. Google's silent revisions and missed cron days both
	// self-heal inside the window.
	WindowDays int
	LagDays    int
	// ReportingZone is the IANA zone the source reports its days in (GSC:
	// "America/Los_Angeles" — Pacific-Time days, not UTC). "" = UTC. Every
	// settled-date/window computation goes through the clock in this zone.
	ReportingZone string
	// BackfillDays is how much history one backfill covers, counted back from
	// the latest settled date (GSC: ~16 months, the API's retention). 0 = the
	// source has no backfill (StartBackfill refuses it).
	BackfillDays int
}

// Source is one analytics data source. Rules:
//   - Normalize is a PURE function of (raw, nctx) — no DB writes, no API
//     calls. That is what makes replay possible and safe.
//   - Dim/metric key names are per-source constants in the source package —
//     the only discipline replacing compile-time struct safety. Renaming one
//     changes fact identity and REQUIRES a full-history replay of the source.
type Source interface {
	Name() string // "gsc"

	// Connection lifecycle (used by the settings endpoints):
	RequiresConnection() bool // gsc: true; dfs_domain_rating: false (our creds)
	VerifyConnection(ctx context.Context, entity *models.WebEntity) (config map[string]string, err error)

	// Ingest lifecycle (used by the orchestrator):
	Schedule() SourceSchedule
	FetchWindow(ctx context.Context, entity *models.WebEntity, window DateWindow) ([]RawPull, error)
	Normalize(ctx context.Context, raw *models.AnalyticsRaw, nctx NormalizeContext) ([]models.AnalyticsFact, error)
	// MergePolicy is the per-metric collision rule for the normalization fold
	// (merge.go); a metric missing from the map folds with SUM.
	MergePolicy() map[string]MergeOp
}
