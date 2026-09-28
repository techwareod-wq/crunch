package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

type fakeSource struct {
	name  string
	sched SourceSchedule
}

func (f *fakeSource) Name() string             { return f.name }
func (f *fakeSource) RequiresConnection() bool { return true }
func (f *fakeSource) VerifyConnection(ctx context.Context, entity *models.WebEntity) (map[string]string, error) {
	return nil, nil
}
func (f *fakeSource) Schedule() SourceSchedule { return f.sched }
func (f *fakeSource) FetchWindow(ctx context.Context, entity *models.WebEntity, window DateWindow) ([]RawPull, error) {
	return nil, nil
}
func (f *fakeSource) Normalize(ctx context.Context, raw *models.AnalyticsRaw, nctx NormalizeContext) ([]models.AnalyticsFact, error) {
	return nil, nil
}
func (f *fakeSource) MergePolicy() map[string]MergeOp { return nil }

func withClock(t *testing.T, instant string) {
	t.Helper()
	fixed, err := time.Parse(time.RFC3339, instant)
	if err != nil {
		t.Fatalf("parse clock instant: %v", err)
	}
	prev := clockNow
	clockNow = func() time.Time { return fixed }
	t.Cleanup(func() { clockNow = prev })
}

func gscLikeSource() *fakeSource {
	return &fakeSource{name: "gsc", sched: SourceSchedule{
		Cadence: "daily", WindowDays: 4, LagDays: 2, ReportingZone: "America/Los_Angeles",
	}}
}

func TestIngestWindow_DailyPacificDays(t *testing.T) {
	src := gscLikeSource()

	// 10:00 UTC = 03:00 PDT — Pacific day matches the UTC date.
	withClock(t, "2026-08-23T10:00:00Z")
	if got := IngestWindow(src); got.From != "2026-08-18" || got.To != "2026-08-21" {
		t.Errorf("IngestWindow = %+v, want 2026-08-18..2026-08-21", got)
	}
	if got := LatestSettledDate(src); got != "2026-08-21" {
		t.Errorf("LatestSettledDate = %q, want 2026-08-21", got)
	}
}

func TestIngestWindow_DailyPacificDayBehindUTC(t *testing.T) {
	src := gscLikeSource()

	// 05:00 UTC = 22:00 PDT the previous day — the Pacific day is still
	// 2026-08-22, so the window must NOT use the UTC date.
	withClock(t, "2026-08-23T05:00:00Z")
	if got := IngestWindow(src); got.From != "2026-08-17" || got.To != "2026-08-20" {
		t.Errorf("IngestWindow = %+v, want 2026-08-17..2026-08-20", got)
	}
}

func TestIngestWindow_MonthlyFirstOfMonth(t *testing.T) {
	src := &fakeSource{name: "dfs_domain_rating", sched: SourceSchedule{Cadence: "monthly", DayOfMonth: 1}}
	withClock(t, "2026-08-23T10:00:00Z")
	if got := IngestWindow(src); got.From != "2026-08-01" || got.To != "2026-08-01" {
		t.Errorf("IngestWindow = %+v, want single date 2026-08-01", got)
	}
}

func TestClampRange(t *testing.T) {
	src := gscLikeSource()
	withClock(t, "2026-08-23T10:00:00Z") // settled = 2026-08-21

	if got := ClampRange(src, "2026-08-01", "2026-08-30"); got.To != "2026-08-21" || got.From != "2026-08-01" {
		t.Errorf("ClampRange future to = %+v, want to clamped to 2026-08-21", got)
	}
	if got := ClampRange(src, "2026-08-25", "2026-08-30"); got.From != "2026-08-21" || got.To != "2026-08-21" {
		t.Errorf("ClampRange all-future = %+v, want collapsed to 2026-08-21", got)
	}
	if got := ClampRange(src, "2026-08-01", ""); got.To != "2026-08-21" {
		t.Errorf("ClampRange empty to = %+v, want to = 2026-08-21", got)
	}
}

func gscLikePolicy() map[string]MergeOp {
	return map[string]MergeOp{
		"clicks":      MergeSum(),
		"impressions": MergeSum(),
		"position":    MergeWeighted("impressions"),
		"ctr":         MergeRecompute("clicks", "impressions"),
	}
}

func TestMergeFacts_FoldsCollisions(t *testing.T) {
	dims := map[string]string{"page": "https://a.com/x"}
	facts := []models.AnalyticsFact{
		{Grain: "page", Date: "2026-08-20", Dims: dims,
			Metrics: map[string]float64{"clicks": 2, "impressions": 100, "ctr": 0.02, "position": 5}},
		{Grain: "page", Date: "2026-08-20", Dims: dims,
			Metrics: map[string]float64{"clicks": 3, "impressions": 300, "ctr": 0.01, "position": 10}},
	}
	merged := mergeFacts(facts, gscLikePolicy())
	if len(merged) != 1 {
		t.Fatalf("mergeFacts returned %d facts, want 1", len(merged))
	}
	m := merged[0].Metrics
	if m["clicks"] != 5 || m["impressions"] != 400 {
		t.Errorf("summed counts = clicks %v impressions %v, want 5/400", m["clicks"], m["impressions"])
	}
	if want := (5.0*100 + 10.0*300) / 400; m["position"] != want {
		t.Errorf("weighted position = %v, want %v", m["position"], want)
	}
	if want := 5.0 / 400; m["ctr"] != want {
		t.Errorf("recomputed ctr = %v, want %v", m["ctr"], want)
	}
}

func TestMergeFacts_NonCollidedPassThrough(t *testing.T) {
	facts := []models.AnalyticsFact{
		{Grain: "page", Date: "2026-08-20", Dims: map[string]string{"page": "https://a.com/x"},
			Metrics: map[string]float64{"clicks": 2, "impressions": 100, "ctr": 0.02, "position": 5}},
		{Grain: "page", Date: "2026-08-21", Dims: map[string]string{"page": "https://a.com/x"},
			Metrics: map[string]float64{"clicks": 4, "impressions": 100, "ctr": 0.04, "position": 3}},
	}
	merged := mergeFacts(facts, gscLikePolicy())
	if len(merged) != 2 {
		t.Fatalf("mergeFacts returned %d facts, want 2 (different dates never fold)", len(merged))
	}
	// Source-reported ratios survive verbatim on non-collided rows.
	if merged[0].Metrics["ctr"] != 0.02 || merged[1].Metrics["ctr"] != 0.04 {
		t.Errorf("pass-through mutated ctr: %v / %v", merged[0].Metrics["ctr"], merged[1].Metrics["ctr"])
	}
}

func TestMergeFacts_DefaultSumAndLast(t *testing.T) {
	facts := []models.AnalyticsFact{
		{Grain: "domain_rating", Date: "2026-08-01", Metrics: map[string]float64{"rating": 40, "probes": 1}},
		{Grain: "domain_rating", Date: "2026-08-01", Metrics: map[string]float64{"rating": 42, "probes": 1}},
	}
	merged := mergeFacts(facts, map[string]MergeOp{"rating": MergeLast()})
	if len(merged) != 1 {
		t.Fatalf("mergeFacts returned %d facts, want 1", len(merged))
	}
	if merged[0].Metrics["rating"] != 42 {
		t.Errorf("last rating = %v, want 42", merged[0].Metrics["rating"])
	}
	if merged[0].Metrics["probes"] != 2 {
		t.Errorf("unlisted metric should default to sum: probes = %v, want 2", merged[0].Metrics["probes"])
	}
}
