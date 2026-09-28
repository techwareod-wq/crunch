package dto

// Keyword status values surfaced on the /keywords screen. "queued" is the
// only one specific to this endpoint (means: no ScheduledArticle exists yet);
// the rest mirror models.ScheduledArticleStatus string labels so the wire
// format is shared with the calendar / dashboard. "error" is the same
// wire-only label the scheduled-articles read path emits when the linked
// WebEntityMasterContext is in CGEStatusError.
const (
	KeywordStatusQueued = "queued"
	KeywordStatusError  = "error"
)

// Usage filter values accepted by the keyword-search endpoint. A keyword is
// "used" iff at least one ScheduledArticle points at it — the same derived
// join behind the status field, collapsed to a boolean so the picker can
// filter without enumerating lifecycle statuses. Empty and "all" both mean
// no usage filter.
const (
	KeywordUsageAll    = "all"
	KeywordUsageUsed   = "used"
	KeywordUsageUnused = "unused"
)

// ParseKeywordUsage normalizes the keyword-search usage query param. "" and
// "all" both mean no filter and normalize to ""; "used"/"unused" pass through;
// anything else is rejected so typos fail loudly instead of silently returning
// an unfiltered list.
func ParseKeywordUsage(raw string) (string, bool) {
	switch raw {
	case "", KeywordUsageAll:
		return "", true
	case KeywordUsageUsed, KeywordUsageUnused:
		return raw, true
	default:
		return "", false
	}
}

type KeywordDataResponse struct {
	Usage         Usage        `json:"usage"`
	TotalKeywords int          `json:"totalKeywords"`
	TotalClusters int          `json:"totalClusters"`
	Clusters      []ClusterDTO `json:"clusters"`
}

// Usage carries the caps the dashboard renders gates from. The trial fields
// (ArticlesUsed/MaxArticles/MaxKeywords/ClusterCount) are zero outside trial
// mode — a zero max means "uncapped", so the FE hides the gate. SIEMode and
// UpgradeState let the FE distinguish trial, expanding (post-upgrade keyword
// growth in flight), and full states without a second endpoint.
type Usage struct {
	ManualUsed  int `json:"manualUsed"`
	ManualLimit int `json:"manualLimit"`
	// ArticlesUsed counts CGE generations started by this user (the trial
	// article cap's derived counter); MaxArticles is the trial cap (0 = uncapped).
	ArticlesUsed int `json:"articlesUsed"`
	MaxArticles  int `json:"maxArticles"`
	// MaxKeywords / ClusterCount are the trial pipeline's persisted-keyword and
	// cluster sizing (0 = full mode, uncapped / 6–10 range).
	MaxKeywords  int    `json:"maxKeywords"`
	ClusterCount int    `json:"clusterCount"`
	SIEMode      string `json:"sieMode"`      // "trial" | "full"
	UpgradeState string `json:"upgradeState"` // "" | "expanding" | "complete"
}

type ClusterDTO struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	KeywordCount    int        `json:"keywordCount"`
	PillarPublished bool       `json:"pillarPublished"`
	Pillar          KeywordDTO `json:"pillar"`
	// Supporting holds only the first page of the cluster's supporting keywords
	// (page size: values.siteIntelligence.supportingKeywordsPageSize). Fetch
	// further pages via the cluster-supporting endpoint. SupportingTotal is the
	// full count so the client can compute how many pages exist.
	Supporting      []KeywordDTO `json:"supporting"`
	SupportingTotal int          `json:"supportingTotal"`
}

// ClusterSupportingResponse is one page of a single cluster's supporting
// keywords, returned by GET /v1/site-intelligence/cluster-supporting. Clusters
// are addressed by their index in the web entity context's cluster list (their
// IDs are LLM-generated and not guaranteed unique). Page is zero-based.
type ClusterSupportingResponse struct {
	ClusterIndex    int          `json:"clusterIndex"`
	Page            int          `json:"page"`
	PageSize        int          `json:"pageSize"`
	SupportingTotal int          `json:"supportingTotal"`
	Supporting      []KeywordDTO `json:"supporting"`
}

// KeywordSearchResponse is the result of GET /v1/site-intelligence/keyword-search.
// It searches every keyword in every cluster, not just the first page each
// cluster embeds in the keyword-data payload, so a client can offer a complete
// keyword search without pulling every page down first.
//
// Total counts the keywords that matched before Limit was applied, so a client
// showing a capped list can tell the user there is more behind the query.
type KeywordSearchResponse struct {
	Query    string                   `json:"query"`
	Status   string                   `json:"status"`
	Usage    string                   `json:"usage"`
	Total    int                      `json:"total"`
	Limit    int                      `json:"limit"`
	Keywords []KeywordSearchResultDTO `json:"keywords"`
}

// KeywordSearchResultDTO is a keyword plus the cluster it sits in. Search
// results span clusters, so — unlike the keyword-data payload, where the cluster
// is implied by nesting — each hit has to name its own cluster.
type KeywordSearchResultDTO struct {
	KeywordDTO
	Cluster      string `json:"cluster"`
	ClusterIndex int    `json:"clusterIndex"`
}

type KeywordDTO struct {
	ID           string  `json:"id"`
	Keyword      string  `json:"keyword"`
	Funnel       string  `json:"funnel"`
	Volume       int     `json:"volume"`
	Difficulty   int     `json:"difficulty"`
	CPC          float64 `json:"cpc"`
	Score        float64 `json:"score"`
	Status       string  `json:"status"`
	ScheduledFor *string `json:"scheduledFor"`
	// Used reports whether any article exists for this keyword; ArticleCount is
	// how many. Status only reflects the latest article, so with reuse enabled
	// these are the fields to badge/filter on, not Status == "queued".
	Used          bool `json:"used"`
	ArticleCount  int  `json:"articleCount"`
	ManuallyAdded bool `json:"manuallyAdded"`
	// Completed is false while a manually-added keyword is still being enriched
	// (funnel/cluster/score) asynchronously, true once enrichment finishes.
	Completed bool `json:"completed"`
}

// AddManualKeywordRequest is the body of POST /v1/site-intelligence/manual-keyword.
type AddManualKeywordRequest struct {
	WebEntityID string `json:"webEntityId"`
	Keyword     string `json:"keyword"`
}

// AddManualKeywordResponse covers two outcomes of POST /manual-keyword:
//
//   - status "enriching" (HTTP 202): the exact keyword had DataForSEO data, so a
//     stub keyword was inserted and the enrichment SQS message dispatched. The
//     keyword's funnel/cluster/score are resolved asynchronously; the FE polls
//     /keyword-data until the keyword appears in a cluster with completed=true.
//   - status "suggestions" (HTTP 200): the exact keyword had no reliable data, so
//     nothing was inserted or dispatched. Suggestions holds clickable
//     alternatives the user can look up instead.
//
// Keyword is a pointer so a suggestions-only response omits it entirely rather
// than serialising an empty keyword object.
type AddManualKeywordResponse struct {
	Keyword     *KeywordDTO               `json:"keyword,omitempty"`
	LowVolume   bool                      `json:"lowVolume"`
	Status      string                    `json:"status"`
	Message     string                    `json:"message,omitempty"`
	Suggestions []ManualKeywordSuggestion `json:"suggestions,omitempty"`
}

// ManualKeywordSuggestion is an alternative keyword returned when the typed
// keyword has no reliable DataForSEO data. Source records which fallback
// endpoint produced it: "keyword_suggestions" or "related_keywords".
type ManualKeywordSuggestion struct {
	Keyword           string  `json:"keyword"`
	Volume            int     `json:"volume"`
	CPC               float64 `json:"cpc,omitempty"`
	KeywordDifficulty int     `json:"keywordDifficulty"`
	Intent            string  `json:"intent,omitempty"`
	Source            string  `json:"source"`
}
