package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Request helpers
type FilterField string

const (
	FilterRankGroup FilterField = "ranked_serp_element.serp_item.rank_group"

	// Keyword Ideas API filter fields (metrics live at the item root).
	FilterSearchVolume      FilterField = "keyword_info.search_volume"
	FilterCPC               FilterField = "keyword_info.cpc"
	FilterCompetitionLevel  FilterField = "keyword_info.competition_level"
	FilterKeywordDifficulty FilterField = "keyword_properties.keyword_difficulty"
	FilterHighTopOfPageBid  FilterField = "keyword_info.high_top_of_page_bid"

	// Ranked Keywords API filter fields. Unlike keyword_ideas, ranked_keywords
	// nests the keyword metrics under keyword_data.* and exposes the SERP element
	// under ranked_serp_element.*.
	FilterRankedSearchVolume      FilterField = "keyword_data.keyword_info.search_volume"
	FilterRankedKeywordDifficulty FilterField = "keyword_data.keyword_properties.keyword_difficulty"
	FilterRankedKeyword           FilterField = "keyword_data.keyword"
	FilterRankedMainIntent        FilterField = "keyword_data.search_intent_info.main_intent"
	FilterSerpItemURL             FilterField = "ranked_serp_element.serp_item.url"
)

type FilterOperator string

const (
	FilterOpEqual          FilterOperator = "="
	FilterOpNotEqual       FilterOperator = "<>"
	FilterOpLessThan       FilterOperator = "<"
	FilterOpLessOrEqual    FilterOperator = "<="
	FilterOpGreaterThan    FilterOperator = ">"
	FilterOpGreaterOrEqual FilterOperator = ">="
	FilterOpContains       FilterOperator = "contains"
	FilterOpNotContains    FilterOperator = "not_contains"
	FilterOpLike           FilterOperator = "like"
	FilterOpNotLike        FilterOperator = "not_like"
	FilterOpIn             FilterOperator = "in"
)

func NewFilter(field FilterField, op FilterOperator, value interface{}) []interface{} {
	return []interface{}{string(field), string(op), value}
}

// AndFilters joins one or more filter expressions with the DataForSEO "and"
// logical operator, producing the flat mixed array the Labs endpoints expect —
// e.g. [[f1], "and", [f2], "and", [f3]]. A single filter yields [[f1]] (no
// operator), and no filters yields nil so the field is omitted entirely.
func AndFilters(filters ...[]interface{}) []interface{} {
	if len(filters) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(filters)*2-1)
	for i, f := range filters {
		if i > 0 {
			out = append(out, "and")
		}
		out = append(out, f)
	}
	return out
}

type OrderByField string

const (
	OrderBySearchVolume OrderByField = "keyword_data.keyword_info.search_volume"

	// Ranked Keywords API order_by field for the SERP rank position.
	OrderByRankGroup OrderByField = "ranked_serp_element.serp_item.rank_group"

	// Keyword Ideas API order_by fields
	OrderByRelevance              OrderByField = "relevance"
	OrderByIdeasSearchVolume      OrderByField = "keyword_info.search_volume"
	OrderByIdeasCPC               OrderByField = "keyword_info.cpc"
	OrderByIdeasCompetitionLevel  OrderByField = "keyword_info.competition_level"
	OrderByIdeasKeywordDifficulty OrderByField = "keyword_properties.keyword_difficulty"
)

type OrderByDirection string

const (
	OrderAsc  OrderByDirection = "asc"
	OrderDesc OrderByDirection = "desc"
)

func NewOrderBy(field OrderByField, dir OrderByDirection) string {
	return fmt.Sprintf("%s,%s", field, dir)
}

type GetKeywordDataRequest struct {
	Tasks []GetKeywordDataTask `json:"task"`
}

type GetKeywordDataTask struct {
	Target           string   `json:"target,omitempty"`
	LanguageName     string   `json:"language_name"`
	LocationCode     int      `json:"location_code"`
	LoadRankAbsolute bool     `json:"load_rank_absolute,omitempty"`
	Limit            int      `json:"limit"`
	Keywords         []string `json:"keywords,omitempty"`
	// CloselyVariants restricts keyword_ideas to close semantic variants of the
	// seeds rather than the broader topical pool.
	CloselyVariants bool `json:"closely_variants,omitempty"`
	// ItemTypes filters ranked_keywords to specific SERP element types (e.g.
	// "organic"); ignored by keyword_ideas.
	ItemTypes []string `json:"item_types,omitempty"`
	// Filters is the flat DataForSEO filter expression: filter arrays joined by
	// "and"/"or" string operators (build it with AndFilters). Kept as
	// []interface{} rather than [][]interface{} so the logical operators fit.
	Filters []interface{} `json:"filters,omitempty"`
	OrderBy []string      `json:"order_by,omitempty"`
}

// --- Response types for DataForSEO ranked_keywords API ---

type RankedKeywordsResponse struct {
	Version       string               `json:"version"`
	StatusCode    int                  `json:"status_code"`
	StatusMessage string               `json:"status_message"`
	Time          string               `json:"time"`
	Cost          float64              `json:"cost"`
	TasksCount    int                  `json:"tasks_count"`
	TasksError    int                  `json:"tasks_error"`
	Tasks         []RankedKeywordsTask `json:"tasks"`
}

type RankedKeywordsTask struct {
	ID            string                 `json:"id"`
	StatusCode    int                    `json:"status_code"`
	StatusMessage string                 `json:"status_message"`
	Time          string                 `json:"time"`
	Cost          float64                `json:"cost"`
	ResultCount   int                    `json:"result_count"`
	Data          map[string]interface{} `json:"data"`
	Result        []RankedKeywordsResult `json:"result"`
}

type RankedKeywordsResult struct {
	Target       string              `json:"target"`
	LocationCode int                 `json:"location_code"`
	ItemsCount   int                 `json:"items_count"`
	Items        []RankedKeywordItem `json:"items"`
}

type RankedKeywordItem struct {
	Keyword           string            `json:"keyword"`
	KeywordData       KeywordData       `json:"keyword_data"`
	RankedSerpElement RankedSerpElement `json:"ranked_serp_element"`
}

type KeywordData struct {
	Keyword           string             `json:"keyword"`
	LocationCode      int                `json:"location_code"`
	LanguageCode      string             `json:"language_code"`
	KeywordInfo       KeywordInfo        `json:"keyword_info"`
	KeywordProperties *KeywordProperties `json:"keyword_properties"`
	SearchIntentInfo  *SearchIntentInfo  `json:"search_intent_info"`
}

type KeywordInfo struct {
	CPC               float64            `json:"cpc"`
	SearchVolume      int                `json:"search_volume"`
	MonthlySearches   []MonthlySearch    `json:"monthly_searches"`
	SearchVolumeTrend *SearchVolumeTrend `json:"search_volume_trend"`
}

type MonthlySearch struct {
	Year         int `json:"year"`
	Month        int `json:"month"`
	SearchVolume int `json:"search_volume"`
}

type SearchVolumeTrend struct {
	Monthly   int `json:"monthly"`
	Quarterly int `json:"quarterly"`
	Yearly    int `json:"yearly"`
}

type KeywordProperties struct {
	KeywordDifficulty *int `json:"keyword_difficulty"`
}

type SearchIntentInfo struct {
	MainIntent    string   `json:"main_intent"`
	ForeignIntent []string `json:"foreign_intent"`
}

type RankedSerpElement struct {
	SerpItem            SerpItem `json:"serp_item"`
	CheckURL            string   `json:"check_url"`
	SerpItemTypes       []string `json:"serp_item_types"`
	SEResultsCount      int64    `json:"se_results_count"`
	KeywordDifficulty   int      `json:"keyword_difficulty"`
	IsLost              bool     `json:"is_lost"`
	LastUpdatedTime     string   `json:"last_updated_time"`
	PreviousUpdatedTime string   `json:"previous_updated_time"`
}

type SerpItem struct {
	RankAbsolute int    `json:"rank_absolute"`
	Description  string `json:"description"`
	URL          string `json:"url"`
}

// --- Response types for DataForSEO keyword_ideas API ---

type KeywordIdeasResponse struct {
	Version       string             `json:"version"`
	StatusCode    int                `json:"status_code"`
	StatusMessage string             `json:"status_message"`
	Time          string             `json:"time"`
	Cost          float64            `json:"cost"`
	TasksCount    int                `json:"tasks_count"`
	TasksError    int                `json:"tasks_error"`
	Tasks         []KeywordIdeasTask `json:"tasks"`
}

type KeywordIdeasTask struct {
	ID            string                 `json:"id"`
	StatusCode    int                    `json:"status_code"`
	StatusMessage string                 `json:"status_message"`
	Time          string                 `json:"time"`
	Cost          float64                `json:"cost"`
	ResultCount   int                    `json:"result_count"`
	Data          map[string]interface{} `json:"data"`
	Result        []KeywordIdeasResult   `json:"result"`
}

type KeywordIdeasResult struct {
	LocationCode int           `json:"location_code"`
	LanguageCode string        `json:"language_code"`
	ItemsCount   int           `json:"items_count"`
	Items        []KeywordData `json:"items"`
}

// --- Request/Response types for DataForSEO SERP organic/live/regular API ---

type GetSerpResultsRequest struct {
	Tasks []SerpTask `json:"task"`
}

type SerpTask struct {
	Keyword      string `json:"keyword"`
	LocationCode int    `json:"location_code"`
	LanguageCode string `json:"language_code"`
	Depth        int    `json:"depth"`
}

type SerpResponse struct {
	Version       string      `json:"version"`
	StatusCode    int         `json:"status_code"`
	StatusMessage string      `json:"status_message"`
	Time          string      `json:"time"`
	Cost          float64     `json:"cost"`
	TasksCount    int         `json:"tasks_count"`
	TasksError    int         `json:"tasks_error"`
	Tasks         []SerpTask2 `json:"tasks"`
}

type SerpTask2 struct {
	ID            string                 `json:"id"`
	StatusCode    int                    `json:"status_code"`
	StatusMessage string                 `json:"status_message"`
	Time          string                 `json:"time"`
	Cost          float64                `json:"cost"`
	ResultCount   int                    `json:"result_count"`
	Data          map[string]interface{} `json:"data"`
	Result        []SerpResult           `json:"result"`
}

type SerpResult struct {
	Keyword      string      `json:"keyword"`
	LocationCode int         `json:"location_code"`
	LanguageCode string      `json:"language_code"`
	ItemsCount   int         `json:"items_count"`
	Items        []SerpItem2 `json:"items"`
}

type SerpItem2 struct {
	Type         string `json:"type"`
	RankGroup    int    `json:"rank_group"`
	RankAbsolute int    `json:"rank_absolute"`
	Domain       string `json:"domain"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	URL          string `json:"url"`
}

// --- Request/Response types for DataForSEO SERP organic/live/advanced API ---

type GetAdvancedSerpRequest struct {
	Tasks []AdvancedSerpTask `json:"task"`
}

type AdvancedSerpTask struct {
	Keyword      string `json:"keyword"`
	LocationCode int    `json:"location_code"`
	LanguageCode string `json:"language_code"`
	Depth        int    `json:"depth"`
	SearchParam  string `json:"search_param,omitempty"`
}

type AdvancedSerpResponse struct {
	Version       string                   `json:"version"`
	StatusCode    int                      `json:"status_code"`
	StatusMessage string                   `json:"status_message"`
	Time          string                   `json:"time"`
	Cost          float64                  `json:"cost"`
	TasksCount    int                      `json:"tasks_count"`
	TasksError    int                      `json:"tasks_error"`
	Tasks         []AdvancedSerpResultTask `json:"tasks"`
}

type AdvancedSerpResultTask struct {
	ID         string               `json:"id"`
	StatusCode int                  `json:"status_code"`
	Result     []AdvancedSerpResult `json:"result"`
}

type AdvancedSerpResult struct {
	Keyword    string             `json:"keyword"`
	ItemsCount int                `json:"items_count"`
	Items      []AdvancedSerpItem `json:"items"`
}

type AdvancedSerpItem struct {
	Type         string          `json:"type"` // "organic", "people_also_ask", "featured_snippet", "related_searches", etc.
	RankGroup    int             `json:"rank_group"`
	RankAbsolute int             `json:"rank_absolute"`
	Domain       string          `json:"domain"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	URL          string          `json:"url"`
	RawItems     json.RawMessage `json:"items,omitempty"` // varies by type; use PAAItems() for people_also_ask
}

// PAAItems parses the nested items as people_also_ask elements.
// Returns nil for non-PAA types or on parse failure.
func (a *AdvancedSerpItem) PAAItems() []PAAItem {
	if len(a.RawItems) == 0 {
		return nil
	}
	var items []PAAItem
	if err := json.Unmarshal(a.RawItems, &items); err != nil {
		return nil
	}
	return items
}

type PAAItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// --- Request/Response types for DataForSEO keyword_overview API ---

type GetKeywordOverviewRequest struct {
	Tasks []KeywordOverviewTask `json:"task"`
}

type KeywordOverviewTask struct {
	Keywords     []string `json:"keywords"`
	LanguageName string   `json:"language_name"`
	LocationCode int      `json:"location_code"`
}

type KeywordOverviewResponse struct {
	Version       string                    `json:"version"`
	StatusCode    int                       `json:"status_code"`
	StatusMessage string                    `json:"status_message"`
	Time          string                    `json:"time"`
	Cost          float64                   `json:"cost"`
	TasksCount    int                       `json:"tasks_count"`
	TasksError    int                       `json:"tasks_error"`
	Tasks         []KeywordOverviewRespTask `json:"tasks"`
}

type KeywordOverviewRespTask struct {
	ID            string                  `json:"id"`
	StatusCode    int                     `json:"status_code"`
	StatusMessage string                  `json:"status_message"`
	Time          string                  `json:"time"`
	Cost          float64                 `json:"cost"`
	ResultCount   int                     `json:"result_count"`
	Data          map[string]interface{}  `json:"data"`
	Result        []KeywordOverviewResult `json:"result"`
}

type KeywordOverviewResult struct {
	LocationCode int           `json:"location_code"`
	LanguageCode string        `json:"language_code"`
	ItemsCount   int           `json:"items_count"`
	Items        []KeywordData `json:"items"`
}

// --- Request/Response types for DataForSEO keyword_suggestions API ---

type KeywordSuggestionsRequest struct {
	Tasks []KeywordSuggestionsTask `json:"task"`
}

type KeywordSuggestionsTask struct {
	Keyword            string          `json:"keyword"`
	LanguageName       string          `json:"language_name"`
	LocationCode       int             `json:"location_code"`
	Limit              int             `json:"limit"`
	ExactMatch         bool            `json:"exact_match"`
	IncludeSeedKeyword bool            `json:"include_seed_keyword"`
	Filters            [][]interface{} `json:"filters,omitempty"`
	OrderBy            []string        `json:"order_by,omitempty"`
}

type KeywordSuggestionsResponse struct {
	Version       string                       `json:"version"`
	StatusCode    int                          `json:"status_code"`
	StatusMessage string                       `json:"status_message"`
	Time          string                       `json:"time"`
	Cost          float64                      `json:"cost"`
	TasksCount    int                          `json:"tasks_count"`
	TasksError    int                          `json:"tasks_error"`
	Tasks         []KeywordSuggestionsRespTask `json:"tasks"`
}

type KeywordSuggestionsRespTask struct {
	ID            string                     `json:"id"`
	StatusCode    int                        `json:"status_code"`
	StatusMessage string                     `json:"status_message"`
	Time          string                     `json:"time"`
	Cost          float64                    `json:"cost"`
	ResultCount   int                        `json:"result_count"`
	Data          map[string]interface{}     `json:"data"`
	Result        []KeywordSuggestionsResult `json:"result"`
}

type KeywordSuggestionsResult struct {
	SeedKeyword  string        `json:"seed_keyword"`
	LocationCode int           `json:"location_code"`
	LanguageCode string        `json:"language_code"`
	TotalCount   int           `json:"total_count"`
	ItemsCount   int           `json:"items_count"`
	Items        []KeywordData `json:"items"`
}

// --- Request/Response types for DataForSEO related_keywords API ---

type RelatedKeywordsRequest struct {
	Tasks []RelatedKeywordsTask `json:"task"`
}

type RelatedKeywordsTask struct {
	Keyword      string `json:"keyword"`
	LanguageName string `json:"language_name"`
	LocationCode int    `json:"location_code"`
	Depth        int    `json:"depth"`
	Limit        int    `json:"limit"`
}

type RelatedKeywordsResponse struct {
	Version       string                    `json:"version"`
	StatusCode    int                       `json:"status_code"`
	StatusMessage string                    `json:"status_message"`
	Time          string                    `json:"time"`
	Cost          float64                   `json:"cost"`
	TasksCount    int                       `json:"tasks_count"`
	TasksError    int                       `json:"tasks_error"`
	Tasks         []RelatedKeywordsRespTask `json:"tasks"`
}

type RelatedKeywordsRespTask struct {
	ID            string                  `json:"id"`
	StatusCode    int                     `json:"status_code"`
	StatusMessage string                  `json:"status_message"`
	Time          string                  `json:"time"`
	Cost          float64                 `json:"cost"`
	ResultCount   int                     `json:"result_count"`
	Data          map[string]interface{}  `json:"data"`
	Result        []RelatedKeywordsResult `json:"result"`
}

type RelatedKeywordsResult struct {
	SeedKeyword  string               `json:"seed_keyword"`
	LocationCode int                  `json:"location_code"`
	LanguageCode string               `json:"language_code"`
	TotalCount   int                  `json:"total_count"`
	ItemsCount   int                  `json:"items_count"`
	Items        []RelatedKeywordItem `json:"items"`
}

// RelatedKeywordItem wraps a KeywordData payload. Unlike keyword_suggestions
// (whose items are KeywordData directly), related_keywords nests the metrics
// under keyword_data and adds related-search discovery fields.
type RelatedKeywordItem struct {
	SeType          string      `json:"se_type"`
	KeywordData     KeywordData `json:"keyword_data"`
	Depth           int         `json:"depth"`
	RelatedKeywords []string    `json:"related_keywords"`
}

// --- Response types for DataForSEO locations_and_languages API ---

type LocationsResponse struct {
	Version       string          `json:"version"`
	StatusCode    int             `json:"status_code"`
	StatusMessage string          `json:"status_message"`
	Tasks         []LocationsTask `json:"tasks"`
}

type LocationsTask struct {
	ID            string         `json:"id"`
	StatusCode    int            `json:"status_code"`
	StatusMessage string         `json:"status_message"`
	ResultCount   int            `json:"result_count"`
	Result        []LocationData `json:"result"`
}

type LocationData struct {
	LocationCode       int    `json:"location_code"`
	LocationName       string `json:"location_name"`
	LocationCodeParent *int   `json:"location_code_parent"`
	CountryISOCode     string `json:"country_iso_code"`
	LocationType       string `json:"location_type"`
}

// --- Request/Response types for DataForSEO backlinks/summary/live API ---

type BacklinksSummaryRequest struct {
	Tasks []BacklinksSummaryTask `json:"task"`
}

type BacklinksSummaryTask struct {
	Target            string `json:"target"`
	InternalListLimit int    `json:"internal_list_limit,omitempty"`
	IncludeSubdomains bool   `json:"include_subdomains"`
	// BacklinksFilters is a single flat filter expression, e.g.
	// ["dofollow", "=", true] — note this endpoint takes one filter, not the
	// nested [][]interface{} the DataForSEO Labs keyword endpoints use.
	BacklinksFilters    []interface{} `json:"backlinks_filters,omitempty"`
	BacklinksStatusType string        `json:"backlinks_status_type,omitempty"`
	// RankScale selects the rank normalisation; "one_hundred" scales result.rank
	// to 0–100 (the domain-rating range this service stores).
	RankScale string `json:"rank_scale,omitempty"`
}

type BacklinksSummaryResponse struct {
	Version       string                     `json:"version"`
	StatusCode    int                        `json:"status_code"`
	StatusMessage string                     `json:"status_message"`
	Time          string                     `json:"time"`
	Cost          float64                    `json:"cost"`
	TasksCount    int                        `json:"tasks_count"`
	TasksError    int                        `json:"tasks_error"`
	Tasks         []BacklinksSummaryRespTask `json:"tasks"`
}

type BacklinksSummaryRespTask struct {
	ID            string                   `json:"id"`
	StatusCode    int                      `json:"status_code"`
	StatusMessage string                   `json:"status_message"`
	Time          string                   `json:"time"`
	Cost          float64                  `json:"cost"`
	ResultCount   int                      `json:"result_count"`
	Data          map[string]interface{}   `json:"data"`
	Result        []BacklinksSummaryResult `json:"result"`
}

// BacklinksSummaryResult carries only the fields this service consumes; Rank
// is the domain-authority score used as the user's domain rating. The profile
// fields below were added for the audit engine's Backlinks category
// (summary-level only, decision 7) — additive, so domain-rating callers are
// untouched.
type BacklinksSummaryResult struct {
	Target                   string `json:"target"`
	Rank                     int    `json:"rank"`
	Backlinks                int64  `json:"backlinks"`
	BrokenBacklinks          int64  `json:"broken_backlinks"`
	ReferringDomains         int64  `json:"referring_domains"`
	ReferringMainDomains     int64  `json:"referring_main_domains"`
	ReferringDomainsNofollow int64  `json:"referring_domains_nofollow"`
}

// --- Request/response types for DataForSEO OnPage instant_pages API ---

type InstantPagesRequest struct {
	Tasks []InstantPagesTask `json:"task"`
}

// InstantPagesTask is a live (blocking) single-page crawl. The anti-blocking
// fields exist because the default crawl profile (datacenter IP + crawler UA +
// no JS) trips Cloudflare-style bot walls and returns the challenge page, and
// CSR-only sites return an empty shell.
type InstantPagesTask struct {
	URL string `json:"url"`
	// EnableBrowserRendering fetches the page through a real headless browser.
	// This is the flag that actually defeats JS-based bot checks and renders
	// client-side (Next.js/SPA) markup; it implies extra DataForSEO charges.
	EnableBrowserRendering bool `json:"enable_browser_rendering,omitempty"`
	// EnableJavascript/LoadResources/EnableXHR are implied by browser rendering
	// but set explicitly so the intent survives if the rendering flag is tuned.
	EnableJavascript bool `json:"enable_javascript,omitempty"`
	LoadResources    bool `json:"load_resources,omitempty"`
	EnableXHR        bool `json:"enable_xhr,omitempty"`
	// CustomUserAgent replaces the default DataForSEO crawler UA, which many
	// bot walls reject outright.
	CustomUserAgent string `json:"custom_user_agent,omitempty"`
	// BrowserPreset sets realistic screen dimensions ("desktop", "mobile",
	// "tablet"); headless fetches with no viewport are a bot fingerprint.
	BrowserPreset  string `json:"browser_preset,omitempty"`
	AcceptLanguage string `json:"accept_language,omitempty"`
	// StoreRawHTML keeps the rendered HTML server-side so the raw_html endpoint
	// can return it (limited retention — collect promptly after the crawl).
	StoreRawHTML bool `json:"store_raw_html,omitempty"`
	// SwitchPool routes the crawl through additional proxy pools, countering
	// IP-reputation blocks against DataForSEO's default gateway ranges.
	SwitchPool    bool   `json:"switch_pool,omitempty"`
	IPPoolForScan string `json:"ip_pool_for_scan,omitempty"`
	// DisableCookiePopup suppresses consent overlays so they don't dominate the
	// rendered text.
	DisableCookiePopup bool `json:"disable_cookie_popup,omitempty"`
	// ReturnDespiteTimeout returns partial data instead of an error if the
	// rendered crawl exceeds DataForSEO's 120s internal timeout.
	ReturnDespiteTimeout bool `json:"return_despite_timeout,omitempty"`
}

// The instant_pages/raw_html response types below carry only the fields this
// service consumes (task id for the raw_html follow-up, statuses for
// success/blocked classification, and the HTML itself); the endpoints return
// far more (timings, cost, crawl metadata, onpage scores, meta tags).

type InstantPagesResponse struct {
	Tasks []InstantPagesRespTask `json:"tasks"`
}

type InstantPagesRespTask struct {
	// ID is the crawl's task id — the key raw_html needs to retrieve the
	// stored HTML.
	ID            string               `json:"id"`
	StatusCode    int                  `json:"status_code"`
	StatusMessage string               `json:"status_message"`
	Result        []InstantPagesResult `json:"result"`
}

type InstantPagesResult struct {
	Items []InstantPageItem `json:"items"`
}

type InstantPageItem struct {
	// StatusCode is the HTTP status the crawler received from the target site
	// (a 403 here means the site blocked the crawl even though the DataForSEO
	// task itself succeeded).
	StatusCode int `json:"status_code"`
}

// --- Request/response types for DataForSEO OnPage raw_html API ---

type RawHtmlRequest struct {
	Tasks []RawHtmlTask `json:"task"`
}

// RawHtmlTask retrieves HTML stored by a prior instant_pages crawl that ran
// with store_raw_html; ID is that crawl's task id.
type RawHtmlTask struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

type RawHtmlResponse struct {
	Tasks []RawHtmlRespTask `json:"tasks"`
}

type RawHtmlRespTask struct {
	StatusCode    int             `json:"status_code"`
	StatusMessage string          `json:"status_message"`
	Result        []RawHtmlResult `json:"result"`
}

type RawHtmlResult struct {
	Items []RawHtmlItem `json:"items"`
}

// UnmarshalJSON accepts items as either a JSON array or a single object:
// DataForSEO's raw_html docs show an object ({"items": {"html": ...}}) while
// live responses have been observed as an array ({"items": [{"html": ...}]}).
// Guessing wrong would make the rendered-fetch path silently fail on every
// call, so both shapes decode.
func (r *RawHtmlResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	items := bytes.TrimSpace(wire.Items)
	if len(items) == 0 || string(items) == "null" {
		return nil
	}
	if items[0] == '[' {
		return json.Unmarshal(items, &r.Items)
	}
	var single RawHtmlItem
	if err := json.Unmarshal(items, &single); err != nil {
		return err
	}
	r.Items = []RawHtmlItem{single}
	return nil
}

type RawHtmlItem struct {
	HTML string `json:"html"`
}

// --- OnPage task API (audit engine crawl — task_post → poll summary →
// pages/links/duplicate_content). Unlike the live endpoints above, this API
// is task-based: post a crawl, poll GET on_page/summary/{id} until finished,
// then page through results. ---

type OnPageTaskPostRequest struct {
	Tasks []OnPageTaskPostTask `json:"task"`
}

// OnPageTaskPostTask starts a site crawl. Cost posture: crawl for structure,
// not rendering — StoreRawHTML and LoadResources stay false; the audit's deep
// pass fetches its small page sample through instant_pages separately.
type OnPageTaskPostTask struct {
	Target           string `json:"target"`
	MaxCrawlPages    int    `json:"max_crawl_pages"`
	EnableJavascript bool   `json:"enable_javascript,omitempty"`
	StoreRawHTML     bool   `json:"store_raw_html,omitempty"`
	LoadResources    bool   `json:"load_resources,omitempty"`
}

type OnPageTaskPostResponse struct {
	StatusCode    int                      `json:"status_code"`
	StatusMessage string                   `json:"status_message"`
	Tasks         []OnPageTaskPostTaskResp `json:"tasks"`
}

type OnPageTaskPostTaskResp struct {
	ID            string `json:"id"`
	StatusCode    int    `json:"status_code"`
	StatusMessage string `json:"status_message"`
}

type OnPageSummaryResponse struct {
	StatusCode    int                 `json:"status_code"`
	StatusMessage string              `json:"status_message"`
	Tasks         []OnPageSummaryTask `json:"tasks"`
}

type OnPageSummaryTask struct {
	ID            string                `json:"id"`
	StatusCode    int                   `json:"status_code"`
	StatusMessage string                `json:"status_message"`
	Result        []OnPageSummaryResult `json:"result"`
}

type OnPageSummaryResult struct {
	// CrawlProgress is "in_progress" or "finished" — the poll condition.
	CrawlProgress string             `json:"crawl_progress"`
	CrawlStatus   *OnPageCrawlStatus `json:"crawl_status"`
	DomainInfo    *OnPageDomainInfo  `json:"domain_info"`
	PageMetrics   *OnPagePageMetrics `json:"page_metrics"`
}

type OnPageCrawlStatus struct {
	MaxCrawlPages int `json:"max_crawl_pages"`
	PagesInQueue  int `json:"pages_in_queue"`
	PagesCrawled  int `json:"pages_crawled"`
}

type OnPageDomainInfo struct {
	Name       string          `json:"name"`
	TotalPages int             `json:"total_pages"`
	SSLInfo    *OnPageSSLInfo  `json:"ssl_info"`
	Checks     map[string]bool `json:"checks"` // sitemap, robots_txt, ...
}

type OnPageSSLInfo struct {
	ValidCertificate bool `json:"valid_certificate"`
}

type OnPagePageMetrics struct {
	LinksExternal        int            `json:"links_external"`
	LinksInternal        int            `json:"links_internal"`
	DuplicateTitle       int            `json:"duplicate_title"`
	DuplicateDescription int            `json:"duplicate_description"`
	DuplicateContent     int            `json:"duplicate_content"`
	BrokenLinks          int            `json:"broken_links"`
	NonIndexable         int            `json:"non_indexable"`
	OnPageScore          float64        `json:"onpage_score"`
	Checks               map[string]int `json:"checks"`
}

type OnPagePagesRequest struct {
	Tasks []OnPagePagesTask `json:"task"`
}

type OnPagePagesTask struct {
	ID      string        `json:"id"`
	Limit   int           `json:"limit,omitempty"`
	Offset  int           `json:"offset,omitempty"`
	Filters []interface{} `json:"filters,omitempty"`
}

type OnPagePagesResponse struct {
	StatusCode    int                   `json:"status_code"`
	StatusMessage string                `json:"status_message"`
	Tasks         []OnPagePagesRespTask `json:"tasks"`
}

type OnPagePagesRespTask struct {
	ID            string              `json:"id"`
	StatusCode    int                 `json:"status_code"`
	StatusMessage string              `json:"status_message"`
	Result        []OnPagePagesResult `json:"result"`
}

type OnPagePagesResult struct {
	TotalItemsCount int              `json:"total_items_count"`
	ItemsCount      int              `json:"items_count"`
	Items           []OnPagePageItem `json:"items"`
}

// OnPagePageItem carries the crawled-page fields the audit's crawl collector
// normalizes (the endpoint returns far more).
type OnPagePageItem struct {
	ResourceType string          `json:"resource_type"` // only "html" items are pages
	StatusCode   int             `json:"status_code"`
	URL          string          `json:"url"`
	ClickDepth   int             `json:"click_depth"`
	OnPageScore  float64         `json:"onpage_score"`
	Meta         *OnPagePageMeta `json:"meta"`
	PageTiming   *OnPageTiming   `json:"page_timing"`
	// Checks is the per-page boolean check map (is_https, is_redirect,
	// no_h1_tag, canonical_chain, ...).
	Checks           map[string]bool `json:"checks"`
	DuplicateTitle   bool            `json:"duplicate_title"`
	DuplicateDesc    bool            `json:"duplicate_description"`
	DuplicateContent bool            `json:"duplicate_content"`
}

type OnPagePageMeta struct {
	Title              string              `json:"title"`
	Description        string              `json:"description"`
	Canonical          string              `json:"canonical"`
	InternalLinksCount int                 `json:"internal_links_count"`
	ExternalLinksCount int                 `json:"external_links_count"`
	InboundLinksCount  int                 `json:"inbound_links_count"`
	ImagesCount        int                 `json:"images_count"`
	HTags              map[string][]string `json:"htags"`
	Content            *OnPageContentMeta  `json:"content"`
}

type OnPageContentMeta struct {
	PlainTextWordCount float64 `json:"plain_text_word_count"`
}

type OnPageTiming struct {
	LargestContentfulPaint float64 `json:"largest_contentful_paint"`
	DurationTime           float64 `json:"duration_time"`
}

type OnPageLinksRequest struct {
	Tasks []OnPageLinksTask `json:"task"`
}

type OnPageLinksTask struct {
	ID     string `json:"id"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type OnPageLinksResponse struct {
	StatusCode    int                   `json:"status_code"`
	StatusMessage string                `json:"status_message"`
	Tasks         []OnPageLinksRespTask `json:"tasks"`
}

type OnPageLinksRespTask struct {
	ID            string              `json:"id"`
	StatusCode    int                 `json:"status_code"`
	StatusMessage string              `json:"status_message"`
	Result        []OnPageLinksResult `json:"result"`
}

type OnPageLinksResult struct {
	TotalItemsCount int              `json:"total_items_count"`
	ItemsCount      int              `json:"items_count"`
	Items           []OnPageLinkItem `json:"items"`
}

type OnPageLinkItem struct {
	Type      string `json:"type"`
	LinkFrom  string `json:"link_from"`
	LinkTo    string `json:"link_to"`
	Direction string `json:"direction"` // internal | external
	Dofollow  bool   `json:"dofollow"`
	IsBroken  bool   `json:"is_broken"`
}

type OnPageDuplicateContentRequest struct {
	Tasks []OnPageDuplicateContentTask `json:"task"`
}

// OnPageDuplicateContentTask asks for pages similar to one URL within a
// finished crawl (the audit calls it only for a bounded sample of pages the
// crawl already flagged duplicate_content).
type OnPageDuplicateContentTask struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Limit int    `json:"limit,omitempty"`
}

type OnPageDuplicateContentResponse struct {
	StatusCode    int                        `json:"status_code"`
	StatusMessage string                     `json:"status_message"`
	Tasks         []OnPageDupContentRespTask `json:"tasks"`
}

type OnPageDupContentRespTask struct {
	ID            string                   `json:"id"`
	StatusCode    int                      `json:"status_code"`
	StatusMessage string                   `json:"status_message"`
	Result        []OnPageDupContentResult `json:"result"`
}

type OnPageDupContentResult struct {
	Items []OnPageDupContentItem `json:"items"`
}

type OnPageDupContentItem struct {
	// Similarity is DataForSEO's 0-10 near-duplicate scale.
	Similarity int                   `json:"similarity"`
	Page       *OnPageDupContentPage `json:"page"`
}

type OnPageDupContentPage struct {
	URL string `json:"url"`
}

// --- DataForSEO Labs domain_rank_overview API (audit Domain Overview strip,
// decision 7) ---

type DomainRankOverviewRequest struct {
	Tasks []DomainRankOverviewTask `json:"task"`
}

type DomainRankOverviewTask struct {
	Target       string `json:"target"`
	LocationCode int    `json:"location_code"`
	LanguageName string `json:"language_name,omitempty"`
}

type DomainRankOverviewResponse struct {
	StatusCode    int                          `json:"status_code"`
	StatusMessage string                       `json:"status_message"`
	Tasks         []DomainRankOverviewRespTask `json:"tasks"`
}

type DomainRankOverviewRespTask struct {
	ID            string                     `json:"id"`
	StatusCode    int                        `json:"status_code"`
	StatusMessage string                     `json:"status_message"`
	Result        []DomainRankOverviewResult `json:"result"`
}

type DomainRankOverviewResult struct {
	Target     string                   `json:"target"`
	ItemsCount int                      `json:"items_count"`
	Items      []DomainRankOverviewItem `json:"items"`
}

type DomainRankOverviewItem struct {
	Metrics *DomainRankMetrics `json:"metrics"`
}

type DomainRankMetrics struct {
	Organic *DomainRankOrganic `json:"organic"`
}

// DomainRankOrganic is the organic ranking-position distribution + ETV.
type DomainRankOrganic struct {
	Pos1      int64   `json:"pos_1"`
	Pos2_3    int64   `json:"pos_2_3"`
	Pos4_10   int64   `json:"pos_4_10"`
	Pos11_20  int64   `json:"pos_11_20"`
	Pos21_30  int64   `json:"pos_21_30"`
	Pos31_40  int64   `json:"pos_31_40"`
	Pos41_50  int64   `json:"pos_41_50"`
	Pos51_60  int64   `json:"pos_51_60"`
	Pos61_70  int64   `json:"pos_61_70"`
	Pos71_80  int64   `json:"pos_71_80"`
	Pos81_90  int64   `json:"pos_81_90"`
	Pos91_100 int64   `json:"pos_91_100"`
	Count     int64   `json:"count"`
	ETV       float64 `json:"etv"`
}
