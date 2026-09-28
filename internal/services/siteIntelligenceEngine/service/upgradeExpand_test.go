package service

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestNormalizeKeywordText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Best CRM  Software ", "best crm software"},
		{"how-to guide", "how to guide"},
		{"salesforce, sheets", "salesforce sheets"},
		{"What's up?", "what s up"},
		{"CRM", "crm"},
	}
	for _, tc := range cases {
		if got := normalizeKeywordText(tc.in); got != tc.want {
			t.Errorf("normalizeKeywordText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalKeywordKey(t *testing.T) {
	// Variants that must collide.
	same := [][2]string{
		{"crm software", "crm softwares"},                  // plural
		{"crm software", "CRM Software"},                   // case/space
		{"email marketing tools", "tools email marketing"}, // word order
		{"seo tool", "seo tools"},                          // plural
		{"how-to guide", "how to guides"},                  // punctuation + plural
	}
	for _, pair := range same {
		if canonicalKeywordKey(pair[0]) != canonicalKeywordKey(pair[1]) {
			t.Errorf("expected %q and %q to share a canonical key", pair[0], pair[1])
		}
	}

	// Distinct terms that must NOT collide.
	diff := [][2]string{
		{"crm software", "crm platform"},
		{"seo tools", "best seo tools"},
		{"keyword research", "how to do keyword research"},
	}
	for _, pair := range diff {
		if canonicalKeywordKey(pair[0]) == canonicalKeywordKey(pair[1]) {
			t.Errorf("expected %q and %q to keep distinct canonical keys", pair[0], pair[1])
		}
	}

	if canonicalKeywordKey("  -- ") != "" {
		t.Error("punctuation-only keyword must canonicalize to empty")
	}
}

func TestDeduplicateVariants(t *testing.T) {
	kws := []models.Keyword{
		// Variant pair decided by provisional opportunity score, NOT volume:
		// "crm softwares" has 9× the volume but a brutal KD, so the easier
		// singular wins its group.
		{Keyword: "crm software", Volume: 100, KeywordDifficulty: 5},
		{Keyword: "crm softwares", Volume: 900, KeywordDifficulty: 80},
		{Keyword: "crm platform", Volume: 50}, // distinct term → kept
		{Keyword: "seo tools", Volume: 40},
		{Keyword: "seo tool", Volume: 10}, // variant, same KD → lower score → dropped
	}
	out := Deduplicate(prodScoring, 15)(kws)
	if len(out) != 3 {
		t.Fatalf("kept %d keywords, want 3: %+v", len(out), out)
	}
	// First-seen order is preserved; the higher-score variant replaces in place.
	if out[0].Keyword != "crm software" || out[1].Keyword != "crm platform" || out[2].Keyword != "seo tools" {
		t.Errorf("got order %q, %q, %q — want crm software, crm platform, seo tools",
			out[0].Keyword, out[1].Keyword, out[2].Keyword)
	}
}

func TestCountRemoved(t *testing.T) {
	n := 0
	step := CountRemoved(Deduplicate(prodScoring, 15), &n)
	out := step([]models.Keyword{
		{Keyword: "seo tools", Volume: 40},
		{Keyword: "seo tool", Volume: 10}, // variant → dropped, counted
		{Keyword: "crm platform", Volume: 5},
	})
	if len(out) != 2 || n != 1 {
		t.Errorf("kept %d keywords, counted %d removed — want 2 kept, 1 counted", len(out), n)
	}
}

func TestJaccard(t *testing.T) {
	a := tokenSet("best crm software")
	b := tokenSet("best crm software free")
	if got := jaccard(a, a); got != 1.0 {
		t.Errorf("identical sets = %v, want 1.0", got)
	}
	if got := jaccard(a, b); got != 0.75 {
		t.Errorf("3-of-4 overlap = %v, want 0.75", got)
	}
	if got := jaccard(a, tokenSet("")); got != 0 {
		t.Errorf("empty set = %v, want 0", got)
	}
}

func TestDedupeMergedKeywords(t *testing.T) {
	protectedID := primitive.NewObjectID()
	survivorID := primitive.NewObjectID()
	loserID := primitive.NewObjectID()
	existing := []models.Keyword{
		{ID: protectedID, Keyword: "best crm software", Volume: 500},          // protected (has an article)
		{ID: survivorID, Keyword: "email marketing tools", Volume: 300},       // unprotected, outscores its fresh variant → survives
		{ID: loserID, Keyword: "seo tool", Volume: 50, KeywordDifficulty: 80}, // unprotected, outscored by fresh variant → deleted
	}
	candidates := []models.Keyword{
		{Keyword: "Best CRM Software", Volume: 900},               // variant of PROTECTED existing → dropped despite higher score
		{Keyword: "best crm software 2026", Volume: 800},          // Jaccard 3/4 = 0.75 < 0.85 → inserted
		{Keyword: "tools email marketing", Volume: 100},           // weaker variant of stored keyword → dropped
		{Keyword: "seo tools", Volume: 400, KeywordDifficulty: 5}, // stronger variant of stored "seo tool" → inserted, old doc deleted
		{Keyword: "ai seo writer", Volume: 50},                    // new → inserted
	}
	protected := map[primitive.ObjectID]struct{}{protectedID: {}}

	out := dedupeMergedKeywords(candidates, existing, protected, prodScoring, 15)

	got := map[string]bool{}
	for _, kw := range out.insert {
		got[kw.Keyword] = true
	}
	want := []string{"best crm software 2026", "seo tools", "ai seo writer"}
	if len(out.insert) != len(want) {
		t.Fatalf("dedupe inserted %d keywords (%v), want %d", len(out.insert), got, len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected %q to be inserted; got: %v", w, got)
		}
	}
	// Protection beats score: the variant of the article-bearing keyword had
	// almost double the volume and still lost.
	if got["Best CRM Software"] {
		t.Error("variant of a protected existing keyword must be dropped even at higher score")
	}
	// Exactly the outscored, unprotected stored keyword is deleted.
	if len(out.deleteIDs) != 1 || out.deleteIDs[0] != loserID {
		t.Errorf("deleteIDs = %v, want exactly [%s]", out.deleteIDs, loserID.Hex())
	}
}

func TestDedupeMergedKeywordsOldVsOldCollapse(t *testing.T) {
	strongID := primitive.NewObjectID()
	weakID := primitive.NewObjectID()
	existing := []models.Keyword{
		{ID: strongID, Keyword: "keyword research", Volume: 400},
		{ID: weakID, Keyword: "keyword researches", Volume: 20}, // pre-existing stored dupe → cleaned up
	}
	out := dedupeMergedKeywords(nil, existing, nil, prodScoring, 15)
	if len(out.insert) != 0 {
		t.Errorf("nothing to insert, got %v", out.insert)
	}
	if len(out.deleteIDs) != 1 || out.deleteIDs[0] != weakID {
		t.Errorf("deleteIDs = %v, want exactly the weaker stored dupe %s", out.deleteIDs, weakID.Hex())
	}
}

func TestTruncateTopKeywords(t *testing.T) {
	kws := []models.Keyword{
		{Keyword: "low", Volume: 10, CPC: 1},
		{Keyword: "high", Volume: 100, CPC: 1},
		{Keyword: "mid-a", Volume: 50, CPC: 2},
		{Keyword: "mid-b", Volume: 50, CPC: 1},
	}
	out := TruncateTopKeywords(2)(kws)
	if len(out) != 2 {
		t.Fatalf("truncated to %d, want 2", len(out))
	}
	if out[0].Keyword != "high" || out[1].Keyword != "mid-a" {
		t.Errorf("kept %q,%q — want volume desc, CPC desc tiebreak (high, mid-a)", out[0].Keyword, out[1].Keyword)
	}

	// No-op cases: zero cap (full mode) and under-cap sets.
	if got := TruncateTopKeywords(0)(kws); len(got) != 4 {
		t.Errorf("cap 0 must be a no-op, got %d", len(got))
	}
	if got := TruncateTopKeywords(10)(kws); len(got) != 4 {
		t.Errorf("under-cap set must pass through, got %d", len(got))
	}
}

func TestClusterCountRule(t *testing.T) {
	if got := clusterCountRule(3); got != "Create exactly 3 clusters. No more, no fewer." {
		t.Errorf("trial rule = %q", got)
	}
	if got := clusterCountRule(0); got != "Create between 6 and 10 clusters. No fewer than 6, no more than 10." {
		t.Errorf("full rule = %q", got)
	}
}

func TestLimitsFor(t *testing.T) {
	svc := &seoBlogGeneratorSiteIntelligence{}
	svc.values.UserKeywordsLimit = 300
	svc.values.CompetitorKeywordsLimit = 200
	svc.values.ExpandedKeywordsLimit = 900
	svc.values.Trial.UserKeywordsLimit = 100
	svc.values.Trial.CompetitorKeywordsLimit = 40
	svc.values.Trial.ExpandedKeywordsLimit = 120
	svc.values.Trial.MaxPersistedKeywords = 60
	svc.values.Trial.ClusterCount = 3

	trialWEC := &models.WebEntityContext{SIEMode: models.SIEModeTrial}
	l := svc.limitsFor(trialWEC)
	if l.UserKeywordsLimit != 100 || l.CompetitorKeywordsLimit != 40 || l.ExpandedKeywordsLimit != 120 ||
		l.MaxPersistedKeywords != 60 || l.ClusterCount != 3 {
		t.Errorf("trial limits = %+v", l)
	}

	// Missing sie_mode (every pre-trial WEC) = full: full fetch limits, no
	// persistence cap, prompt-default cluster range.
	legacyWEC := &models.WebEntityContext{}
	l = svc.limitsFor(legacyWEC)
	if l.UserKeywordsLimit != 300 || l.CompetitorKeywordsLimit != 200 || l.ExpandedKeywordsLimit != 900 ||
		l.MaxPersistedKeywords != 0 || l.ClusterCount != 0 {
		t.Errorf("full limits = %+v", l)
	}
}
