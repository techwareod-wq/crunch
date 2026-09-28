package gsc

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
)

type fakeGSC struct {
	sites []dto.GSCSite
	rows  map[string][]dto.GSCRow // keyed by joined dimensions
}

func (f *fakeGSC) Enabled() bool       { return true }
func (f *fakeGSC) ClientEmail() string { return "sa@test.iam.gserviceaccount.com" }
func (f *fakeGSC) ListSites(ctx context.Context) ([]dto.GSCSite, error) {
	return f.sites, nil
}
func (f *fakeGSC) QuerySearchAnalytics(ctx context.Context, property string, req dto.GSCQueryRequest) ([]dto.GSCRow, error) {
	return nil, errors.New("not used")
}

func testSource(sites ...dto.GSCSite) *Source {
	return New(&fakeGSC{sites: sites}, config.GSCValues{
		TopQueriesPerDay: 1000, TopPageQueryRowsPerDay: 2500, MaxPagesPerDay: 5000,
	})
}

func entityFor(url string) *models.WebEntity {
	return &models.WebEntity{ID: primitive.NewObjectID(), WebsiteUrl: url}
}

func TestVerifyConnection_PrefersURLPrefixOverDomainProperty(t *testing.T) {
	src := testSource(
		dto.GSCSite{SiteURL: "sc-domain:foo.com", PermissionLevel: "siteOwner"},
		dto.GSCSite{SiteURL: "https://www.foo.com/", PermissionLevel: "siteRestrictedUser"},
	)
	cfg, err := src.VerifyConnection(context.Background(), entityFor("https://www.foo.com"))
	if err != nil {
		t.Fatalf("VerifyConnection: %v", err)
	}
	if cfg[ConfigKeyProperty] != "https://www.foo.com/" {
		t.Errorf("matched %q, want the URL-prefix property (sc-domain inflates subdomain totals)", cfg[ConfigKeyProperty])
	}
}

func TestVerifyConnection_DomainPropertyLastResort(t *testing.T) {
	src := testSource(dto.GSCSite{SiteURL: "sc-domain:foo.com", PermissionLevel: "siteFullUser"})
	cfg, err := src.VerifyConnection(context.Background(), entityFor("foo.com"))
	if err != nil {
		t.Fatalf("VerifyConnection: %v", err)
	}
	if cfg[ConfigKeyProperty] != "sc-domain:foo.com" {
		t.Errorf("matched %q, want sc-domain:foo.com", cfg[ConfigKeyProperty])
	}
}

func TestVerifyConnection_SkipsUnverifiedAndMisses(t *testing.T) {
	src := testSource(dto.GSCSite{SiteURL: "https://foo.com/", PermissionLevel: "siteUnverifiedUser"})
	if _, err := src.VerifyConnection(context.Background(), entityFor("foo.com")); !errors.Is(err, ErrNoPropertyMatch) {
		t.Errorf("err = %v, want ErrNoPropertyMatch (unverified permission cannot be queried)", err)
	}
}

func rawFor(t *testing.T, date, pullName string, rows []dto.GSCRow) *models.AnalyticsRaw {
	t.Helper()
	payload, err := bson.Marshal(gscPayload{Rows: rows})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &models.AnalyticsRaw{Source: SourceName, Date: date, Pull: pullName, Payload: payload}
}

func TestNormalize_GrainsDimsAndArticleStamp(t *testing.T) {
	src := testSource()
	articleID := primitive.NewObjectID()
	nctx := analytics.NormalizeContext{ArticleURLIndex: map[string]primitive.ObjectID{
		"https://foo.com/blog/x": articleID,
	}}

	facts, err := src.Normalize(context.Background(),
		rawFor(t, "2026-08-20", "page", []dto.GSCRow{
			// http variant + trailing slash both normalize onto the stamped URL.
			{Keys: []string{"2026-08-20", "http://FOO.com/blog/x/"}, Clicks: 3, Impressions: 90, CTR: 0.033, Position: 7},
			{Keys: []string{"2026-08-20", "https://foo.com/other"}, Clicks: 1, Impressions: 10, CTR: 0.1, Position: 3},
		}), nctx)
	if err != nil {
		t.Fatalf("Normalize page: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("page facts = %d, want 2", len(facts))
	}
	if facts[0].Grain != GrainPage || facts[0].Dims[DimPage] != "https://foo.com/blog/x" {
		t.Errorf("fact 0 = %+v, want normalized page dim", facts[0])
	}
	if facts[0].Dims[DimArticleID] != articleID.Hex() {
		t.Errorf("article stamp = %q, want %s", facts[0].Dims[DimArticleID], articleID.Hex())
	}
	if _, stamped := facts[1].Dims[DimArticleID]; stamped {
		t.Errorf("non-article page must not carry an article_id dim")
	}
	if facts[0].Metrics[MetClicks] != 3 || facts[0].Metrics[MetImpressions] != 90 {
		t.Errorf("metrics = %+v, want clicks 3 impressions 90", facts[0].Metrics)
	}
}

// The 2026-08-26 finding: headless publishes can't know the public blog path
// prefix, so the exact-URL stamp misses (/blog/<slug> vs stamped <site>/<slug>)
// — the slug index catches any page whose LAST segment equals a published slug.
func TestNormalize_SlugFallbackStamp(t *testing.T) {
	src := testSource()
	articleID := primitive.NewObjectID()
	nctx := analytics.NormalizeContext{
		ArticleURLIndex: map[string]primitive.ObjectID{
			"https://foo.com/reply-this-email-templates": articleID, // stamped without /blog/
		},
		ArticleSlugIndex: map[string]primitive.ObjectID{
			"reply-this-email-templates": articleID,
		},
	}

	facts, err := src.Normalize(context.Background(),
		rawFor(t, "2026-08-20", "page", []dto.GSCRow{
			{Keys: []string{"2026-08-20", "https://foo.com/blog/reply-this-email-templates"}, Clicks: 2, Impressions: 40, CTR: 0.05, Position: 9},
			{Keys: []string{"2026-08-20", "https://foo.com/"}, Clicks: 5, Impressions: 100, CTR: 0.05, Position: 2},
			{Keys: []string{"2026-08-20", "https://foo.com/privacy"}, Clicks: 1, Impressions: 10, CTR: 0.1, Position: 4},
		}), nctx)
	if err != nil {
		t.Fatalf("Normalize page: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("page facts = %d, want 3", len(facts))
	}
	if facts[0].Dims[DimArticleID] != articleID.Hex() {
		t.Errorf("blog page not slug-stamped: dims = %+v", facts[0].Dims)
	}
	for _, i := range []int{1, 2} {
		if _, stamped := facts[i].Dims[DimArticleID]; stamped {
			t.Errorf("fact %d (%s) must not be stamped", i, facts[i].Dims[DimPage])
		}
	}
}

func TestLastPathSegment(t *testing.T) {
	cases := map[string]string{
		"https://foo.com/blog/my-slug": "my-slug",
		"https://foo.com/My-Slug":      "my-slug",
		"https://foo.com/":             "",
		"https://foo.com":              "",
		"https://foo.com/blog/x/":      "x",
	}
	for in, want := range cases {
		if got := lastPathSegment(in); got != want {
			t.Errorf("lastPathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize_SiteAndPageQueryChunk(t *testing.T) {
	src := testSource()

	site, err := src.Normalize(context.Background(),
		rawFor(t, "2026-08-20", "site", []dto.GSCRow{
			{Keys: []string{"2026-08-20"}, Clicks: 42, Impressions: 1000, CTR: 0.042, Position: 12.5},
		}), analytics.NormalizeContext{})
	if err != nil {
		t.Fatalf("Normalize site: %v", err)
	}
	if len(site) != 1 || site[0].Grain != GrainSite || len(site[0].Dims) != 0 {
		t.Fatalf("site facts = %+v, want one dimensionless fact", site)
	}

	// A chunked pull name normalizes under its base grain.
	pq, err := src.Normalize(context.Background(),
		rawFor(t, "2026-08-20", "page_query#1", []dto.GSCRow{
			{Keys: []string{"2026-08-20", "https://foo.com/blog/x", "best foo"}, Clicks: 2, Impressions: 40, CTR: 0.05, Position: 9},
		}), analytics.NormalizeContext{})
	if err != nil {
		t.Fatalf("Normalize page_query chunk: %v", err)
	}
	if len(pq) != 1 || pq[0].Grain != GrainPageQuery || pq[0].Dims[DimQuery] != "best foo" {
		t.Fatalf("page_query facts = %+v, want grain page_query with query dim", pq)
	}
}

func TestBuildPulls_ChunksOversizedPayloads(t *testing.T) {
	// ~300 rows × ~40KB keys ≈ 12MB — must split, never truncate.
	big := make([]dto.GSCRow, 300)
	longKey := string(make([]byte, 40_000))
	for i := range big {
		big[i] = dto.GSCRow{Keys: []string{"2026-08-20", longKey}}
	}
	pulls, err := buildPulls("2026-08-20", "page_query", big)
	if err != nil {
		t.Fatalf("buildPulls: %v", err)
	}
	if len(pulls) < 2 {
		t.Fatalf("oversized payload not chunked: %d pulls", len(pulls))
	}
	total := 0
	for _, p := range pulls {
		if len(p.Payload) > maxRawPayloadBytes {
			t.Errorf("chunk %s still oversize: %d bytes", p.Pull, len(p.Payload))
		}
		if base := basePull(p.Pull); base != "page_query" {
			t.Errorf("chunk name %q does not strip to base pull", p.Pull)
		}
		total += p.RowCount
	}
	if total != len(big) {
		t.Errorf("chunking lost rows: %d of %d", total, len(big))
	}
}

func TestWindowDates(t *testing.T) {
	dates, err := windowDates(analytics.DateWindow{From: "2026-08-18", To: "2026-08-21"})
	if err != nil {
		t.Fatalf("windowDates: %v", err)
	}
	want := []string{"2026-08-18", "2026-08-19", "2026-08-20", "2026-08-21"}
	if len(dates) != len(want) {
		t.Fatalf("dates = %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Fatalf("dates = %v, want %v", dates, want)
		}
	}
}
