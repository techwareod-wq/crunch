package service

import (
	"strconv"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// searchFixture builds two clusters whose supporting keywords run well past the
// 10-per-cluster page the keyword-data payload embeds. That gap is the whole
// reason this endpoint exists: "deep 14" below lives on page 2 and was invisible
// to the picker's old in-memory filter.
func searchFixture(t *testing.T) (*models.WebEntityContext, *keywordLookups) {
	t.Helper()

	keywords := map[primitive.ObjectID]models.Keyword{}
	newKeyword := func(text string, score float64) primitive.ObjectID {
		id := primitive.NewObjectID()
		keywords[id] = models.Keyword{
			ID:               id,
			Keyword:          text,
			Funnel:           models.FunnelStage("TOFU"),
			Volume:           100,
			OpportunityScore: score,
			Completed:        true,
		}
		return id
	}

	pillarA := newKeyword("salesforce to google sheets", 90)
	supportingA := make([]primitive.ObjectID, 0, 20)
	for i := range 20 {
		// Ascending score, so the highest-scoring supporting keywords are the ones
		// deepest in the list — a cap that ignored ranking would drop them.
		supportingA = append(supportingA, newKeyword(deepName(i), float64(i)))
	}

	pillarB := newKeyword("hubspot crm export", 95)
	supportingB := []primitive.ObjectID{newKeyword("hubspot to sheets", 50)}

	wec := &models.WebEntityContext{
		ID: primitive.NewObjectID(),
		Clusters: []models.Cluster{
			{
				ClusterID:            "salesforce-sheets",
				ClusterName:          "Salesforce + Google Sheets",
				PillarKeywordID:      pillarA,
				SupportingKeywordIDs: supportingA,
			},
			{
				ClusterID:            "hubspot",
				ClusterName:          "HubSpot",
				PillarKeywordID:      pillarB,
				SupportingKeywordIDs: supportingB,
			},
		},
	}

	return wec, &keywordLookups{
		keywordByID:      keywords,
		articleByKeyword: map[primitive.ObjectID]*models.ScheduledArticle{},
		erroredArticles:  map[string]struct{}{},
	}
}

func deepName(i int) string {
	return "deep n" + strconv.Itoa(i)
}

func TestBuildKeywordSearchResponse_FindsKeywordsPastTheFirstPage(t *testing.T) {
	wec, lookups := searchFixture(t)

	// "deep n14" sits at index 14 of the supporting list — page 2 of the
	// 10-per-page cluster payload, i.e. exactly what the client could not see.
	target := deepName(14)
	got := buildKeywordSearchResponse(wec, lookups, target, "", "", 50)

	if got.Total != 1 {
		t.Fatalf("expected exactly 1 match for %q, got %d", target, got.Total)
	}
	if got.Keywords[0].Keyword != target {
		t.Fatalf("expected %q, got %q", target, got.Keywords[0].Keyword)
	}
	if got.Keywords[0].Cluster != "Salesforce + Google Sheets" {
		t.Fatalf("expected the hit to carry its cluster name, got %q", got.Keywords[0].Cluster)
	}
	if got.Keywords[0].ClusterIndex != 0 {
		t.Fatalf("expected clusterIndex 0, got %d", got.Keywords[0].ClusterIndex)
	}
}

func TestBuildKeywordSearchResponse_MatchesClusterName(t *testing.T) {
	wec, lookups := searchFixture(t)

	got := buildKeywordSearchResponse(wec, lookups, "hubspot", "", "", 50)

	// The pillar matches on its own text; "hubspot to sheets" matches on text too.
	// A cluster-name hit must admit every keyword in the cluster, so both land.
	if got.Total != 2 {
		t.Fatalf("expected both HubSpot cluster keywords, got %d", got.Total)
	}
	for _, k := range got.Keywords {
		if k.Cluster != "HubSpot" {
			t.Fatalf("expected only HubSpot keywords, got %q in %q", k.Keyword, k.Cluster)
		}
	}
}

func TestBuildKeywordSearchResponse_CapsAtLimitKeepingHighestScores(t *testing.T) {
	wec, lookups := searchFixture(t)

	got := buildKeywordSearchResponse(wec, lookups, "", "", "", 4)

	if got.Limit != 4 {
		t.Fatalf("expected limit 4, got %d", got.Limit)
	}
	if len(got.Keywords) != 4 {
		t.Fatalf("expected 4 results, got %d", len(got.Keywords))
	}
	// Total reports the pre-cap match count so the client can say "showing 4 of N".
	if got.Total != 23 {
		t.Fatalf("expected total 23 (2 pillars + 21 supporting), got %d", got.Total)
	}

	// Ranked by opportunity score, not by position in the cluster: the hubspot
	// pillar (95), the salesforce pillar (90), "hubspot to sheets" (50), and then
	// deep n19 (19) — which sits on page 2 of its cluster and still beats the 19
	// lower-scoring supporting keywords ahead of it in document order.
	want := []string{
		"hubspot crm export",
		"salesforce to google sheets",
		"hubspot to sheets",
		deepName(19),
	}
	for i, w := range want {
		if got.Keywords[i].Keyword != w {
			t.Fatalf("rank %d: expected %q, got %q", i, w, got.Keywords[i].Keyword)
		}
	}
}

func TestBuildKeywordSearchResponse_FiltersByStatus(t *testing.T) {
	wec, lookups := searchFixture(t)

	// Book an article against the top-ranked keyword: it is no longer schedulable,
	// so a status=queued search must not offer it.
	booked := wec.Clusters[1].PillarKeywordID
	lookups.articleByKeyword[booked] = &models.ScheduledArticle{
		ID:        primitive.NewObjectID(),
		KeywordID: booked,
		Status:    models.ScheduledArticleStatusScheduled,
	}

	got := buildKeywordSearchResponse(wec, lookups, "hubspot", sieDto.KeywordStatusQueued, "", 50)

	if got.Total != 1 {
		t.Fatalf("expected the booked keyword to be filtered out, got %d results", got.Total)
	}
	if got.Keywords[0].Keyword != "hubspot to sheets" {
		t.Fatalf("expected the still-queued keyword, got %q", got.Keywords[0].Keyword)
	}

	// Without the status filter it comes back, so the filter is what excluded it.
	all := buildKeywordSearchResponse(wec, lookups, "hubspot", "", "", 50)
	if all.Total != 2 {
		t.Fatalf("expected both keywords with no status filter, got %d", all.Total)
	}
}

func TestBuildKeywordSearchResponse_FiltersByUsage(t *testing.T) {
	wec, lookups := searchFixture(t)

	// Two articles booked on the hubspot pillar: it is "used", everything else
	// is not. Unlike the status filter, usage must not care which lifecycle
	// state the article is in.
	booked := wec.Clusters[1].PillarKeywordID
	lookups.articleByKeyword[booked] = &models.ScheduledArticle{
		ID:        primitive.NewObjectID(),
		KeywordID: booked,
		Status:    models.ScheduledArticleStatusPublished,
	}
	lookups.articleCountByKeyword = map[primitive.ObjectID]int{booked: 2}

	used := buildKeywordSearchResponse(wec, lookups, "hubspot", "", sieDto.KeywordUsageUsed, 50)
	if used.Total != 1 || used.Keywords[0].Keyword != "hubspot crm export" {
		t.Fatalf("usage=used: expected only the booked keyword, got %+v", used.Keywords)
	}
	if !used.Keywords[0].Used || used.Keywords[0].ArticleCount != 2 {
		t.Fatalf("expected used=true articleCount=2, got used=%v articleCount=%d",
			used.Keywords[0].Used, used.Keywords[0].ArticleCount)
	}

	unused := buildKeywordSearchResponse(wec, lookups, "hubspot", "", sieDto.KeywordUsageUnused, 50)
	if unused.Total != 1 || unused.Keywords[0].Keyword != "hubspot to sheets" {
		t.Fatalf("usage=unused: expected only the article-free keyword, got %+v", unused.Keywords)
	}
	if unused.Keywords[0].Used || unused.Keywords[0].ArticleCount != 0 {
		t.Fatalf("expected used=false articleCount=0, got used=%v articleCount=%d",
			unused.Keywords[0].Used, unused.Keywords[0].ArticleCount)
	}

	// No usage filter: both come back, the used one still tagged.
	all := buildKeywordSearchResponse(wec, lookups, "hubspot", "", "", 50)
	if all.Total != 2 {
		t.Fatalf("expected both keywords with no usage filter, got %d", all.Total)
	}
}

func TestBuildKeywordSearchResponse_EmptyQueryReturnsEverything(t *testing.T) {
	wec, lookups := searchFixture(t)

	got := buildKeywordSearchResponse(wec, lookups, "   ", "", "", 100)

	if got.Total != 23 {
		t.Fatalf("expected a blank query to match every keyword, got %d", got.Total)
	}
}

func TestBuildKeywordSearchResponse_NonPositiveLimitFallsBackToDefault(t *testing.T) {
	wec, lookups := searchFixture(t)

	got := buildKeywordSearchResponse(wec, lookups, "", "", "", 0)

	if got.Limit != defaultKeywordSearchLimit {
		t.Fatalf("expected the default limit %d, got %d", defaultKeywordSearchLimit, got.Limit)
	}
}
