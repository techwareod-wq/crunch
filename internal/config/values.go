package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// Values holds runtime tunables and external API base URLs loaded from
// values/<ENVIRONMENT>/values.yaml at startup. It is the YAML-default layer:
// the env-driven loaders (LoadServerConfig, LoadSQSConfig, …) read these as
// their defaults and let a matching env var override. The remaining fields have
// no env var and are consumed directly by providers and services.
//
// See GLOBAL_CONSTANTS.md for the full catalog and which constants moved here.
type Values struct {
	Server            ServerValues            `yaml:"server"`
	Admin             AdminValues             `yaml:"admin"`
	Async             AsyncValues             `yaml:"async"`
	SQS               SQSValues               `yaml:"sqs"`
	Idempotency       IdempotencyValues       `yaml:"idempotency"`
	Webhooks          WebhookValues           `yaml:"webhooks"`
	Paddle            PaddleValues            `yaml:"paddle"`
	S3                S3Values                `yaml:"s3"`
	LLM               LLMValues               `yaml:"llm"`
	APIs              APIValues               `yaml:"apis"`
	ContentGeneration ContentGenerationValues `yaml:"contentGeneration"`
	Scheduling        SchedulingValues        `yaml:"scheduling"`
	Onboarding        OnboardingValues        `yaml:"onboarding"`
	SiteIntelligence  SiteIntelligenceValues  `yaml:"siteIntelligence"`
	Cron              CronValues              `yaml:"cron"`
	Company           CompanyValues           `yaml:"company"`
	GSC               GSCValues               `yaml:"gsc"`
	StyleReplication  StyleReplicationValues  `yaml:"styleReplication"`
	Audit             AuditValues             `yaml:"audit"`
}

// AuditValues configures the SEO/AEO audit engine (internal/audit). Enabled
// is the master switch for the /v1/audit surface (404 masquerade when off,
// drift-allowlisted: live in integration first, dark in prod until launch);
// everything else bounds one run's cost or gates abuse (decisions 10/14/15/16).
type AuditValues struct {
	Enabled bool `yaml:"enabled"`
	// ArtifactTTLDays prunes auditArtifact scratch docs (the report embeds
	// everything user-facing).
	ArtifactTTLDays int                 `yaml:"artifactTTLDays"`
	Crawl           AuditCrawlValues    `yaml:"crawl"`
	Sampling        AuditSampleValues   `yaml:"sampling"`
	Scoring         AuditScoringValues  `yaml:"scoring"`
	Gating          AuditGatingValues   `yaml:"gating"`
	LLM             AuditLLMValues      `yaml:"llm"`
	Recheck         AuditRecheckValues  `yaml:"recheck"`
	Mentions        AuditMentionsValues `yaml:"mentions"`
}

// AuditMentionsValues gates the brand-mention footprint collector (quality
// uplift 3.1 — ~2 extra SERP calls per audit, approved for ALL run kinds
// including the lead funnel). Disabled ⇒ the collector opts out of the
// fan-out and aisearch.brand_footprint skips/renormalizes.
type AuditMentionsValues struct {
	Enabled bool `yaml:"enabled"`
	// SerpDepth is the page-1 depth per mention query (0 → default 20).
	SerpDepth int `yaml:"serpDepth"`
}

// AuditRecheckValues bounds the per-check re-verification loop (scoped
// re-collection + one check re-run; deterministic checks only, so the caps
// bound provider spend, not LLM budget).
type AuditRecheckValues struct {
	PerCheckCooldownMinutes int `yaml:"perCheckCooldownMinutes"`
	MaxPerRunPerDay         int `yaml:"maxPerRunPerDay"`
}

// AuditCrawlValues bounds the OnPage crawl — the page caps are the primary
// OnPage cost lever (decision 14).
type AuditCrawlValues struct {
	TenantPageCap int `yaml:"tenantPageCap"`
	LeadPageCap   int `yaml:"leadPageCap"`
	// PollIntervalSeconds/PollTimeoutSeconds shape the in-handler summary
	// poll; the AUDIT_CRAWL visibility override must exceed the timeout.
	PollIntervalSeconds int `yaml:"pollIntervalSeconds"`
	PollTimeoutSeconds  int `yaml:"pollTimeoutSeconds"`
}

// AuditSampleValues sizes the deep pass (decision 15).
type AuditSampleValues struct {
	DeepPassPages     int `yaml:"deepPassPages"`
	LeadDeepPassPages int `yaml:"leadDeepPassPages"`
	// SXOPages is capped at the run's deep-pass sample size.
	SXOPages int `yaml:"sxoPages"`
}

// AuditScoringValues selects the spec version and the values-controlled
// findings-only demotions (decisions 12/13).
type AuditScoringValues struct {
	SpecVersion string `yaml:"specVersion"`
	// FindingsOnly lists CheckIDs demoted to findings-only; the resolved set
	// is snapshotted per run. Unknown IDs fail the boot.
	FindingsOnly []string `yaml:"findingsOnly"`
}

// AuditGatingValues holds the cadence + lead abuse caps (decisions 10/16).
type AuditGatingValues struct {
	PaidCooldownDays        int `yaml:"paidCooldownDays"`
	LeadRunsPerIPPerDay     int `yaml:"leadRunsPerIPPerDay"`
	LeadDomainCooldownWeeks int `yaml:"leadDomainCooldownWeeks"`
	// LeadRunsPerDay is the global daily lead-run circuit breaker.
	LeadRunsPerDay int `yaml:"leadRunsPerDay"`
}

// AuditLLMValues picks the models for the two audit LLM calls (decision 17:
// one Content judgment call + one short narrative call). The lead endpoints
// carry no body-cap knob: the shared JSON deserializer's built-in cap bounds
// them like every other JSON route.
type AuditLLMValues struct {
	JudgmentModel      string `yaml:"judgmentModel"`
	NarrativeModel     string `yaml:"narrativeModel"`
	JudgmentMaxTokens  int    `yaml:"judgmentMaxTokens"`
	NarrativeMaxTokens int    `yaml:"narrativeMaxTokens"`
	// JudgmentFallbackModel gets ONE retry when the primary judgment call
	// fails for good (quality uplift 4.1 — the judgment failed silently on a
	// real run; a different model id dodges model-specific outages). Empty →
	// Haiku 4.5.
	JudgmentFallbackModel string `yaml:"judgmentFallbackModel"`
}

// StyleReplicationValues configures the style replication surface (learn tone/
// structure/image style from ~10 existing articles). Enabled is the master
// switch for the /v1/style-replication routes (404 masquerade when off, same
// lifecycle as gsc.enabled); the rest bound one run's cost.
type StyleReplicationValues struct {
	Enabled bool `yaml:"enabled"`
	// MaxRunsPerDay rate-limits runs per web entity per rolling day; errored
	// runs don't count.
	MaxRunsPerDay int `yaml:"maxRunsPerDay"`
	// MinUrls is the hard floor on the finalized source-article list (the
	// frontend's under-3 soft warning is separate). 0/absent behaves as 1.
	MinUrls int `yaml:"minUrls"`
	// MaxUrls caps the finalized source-article list (also the discovery cap).
	MaxUrls int `yaml:"maxUrls"`
	// MaxImagesPerURL caps candidate images extracted per scraped article.
	MaxImagesPerURL int `yaml:"maxImagesPerURL"`
	// MaxSynthesisImages caps how many approved images the vision synthesis
	// call actually receives.
	MaxSynthesisImages int `yaml:"maxSynthesisImages"`
	// MaxContentCharsPerURL caps the body text stored per scraped article.
	MaxContentCharsPerURL int `yaml:"maxContentCharsPerURL"`
	// FetchTimeoutSeconds bounds each per-URL fetch during scraping and the
	// sitemap calls during discovery.
	FetchTimeoutSeconds int `yaml:"fetchTimeoutSeconds"`
}

// GSCValues configures the Google Search Console analytics source (the
// analytics engine's "gsc" source and its /v1/analytics surface). The top-N
// caps bound what each daily pull requests from the Search Analytics API on
// huge sites; trimmed fetches log the dropped-row count (no silent caps).
type GSCValues struct {
	// Enabled is the master switch for the GSC analytics surface: false
	// darkens every /v1/analytics/* route (404 masquerade) and the verify
	// flow. Same lifecycle as the cron flags: live in integration first,
	// dark in prod until proven.
	Enabled bool `yaml:"enabled"`
	// TopQueriesPerDay caps rows requested for the query grain per day.
	TopQueriesPerDay int `yaml:"topQueriesPerDay"`
	// TopPageQueryRowsPerDay caps rows requested for the page_query grain per day.
	TopPageQueryRowsPerDay int `yaml:"topPageQueryRowsPerDay"`
	// MaxPagesPerDay caps rows requested for the page grain per day.
	MaxPagesPerDay int `yaml:"maxPagesPerDay"`
	// BackfillDays is how much history a backfill fetches, counted back from
	// the latest settled date (480 ≈ the API's ~16-month retention). 0/absent
	// disables backfill for the source.
	BackfillDays int `yaml:"backfillDays"`
}

// CompanyValues configures the company-tenancy surface (tenancy plan P4/P6):
// the invite-accept link template plus its token lifetime. Joining a company
// happens ONLY through this emailed link. The template points at the
// FRONTEND, which differs per deployment — the YAML default carries the
// per-env URL and the env var (COMPANY_INVITE_URL_TEMPLATE) remains an
// override. An empty template fails closed: no link is minted, the flow
// reports the URL to the dashboard response only.
type CompanyValues struct {
	// Enabled is the master switch for the user-facing company surface: false
	// darkens every /v1/company/* route (JSON 404 via WithCompanyFeature). The
	// data layer underneath — personal-company minting, boot indexes, Paddle
	// webhook seat sync, seat entitlement projection — keeps running
	// regardless, so flipping ON later needs no migration.
	Enabled bool `yaml:"enabled"`
	// InviteURLTemplate renders the invite-accept link with {token}
	// substituted (the accept endpoint resolves the membership from the
	// token, D19).
	InviteURLTemplate string `yaml:"inviteUrlTemplate"`
	// InviteTokenTTLHours is how long an accept token stays claimable.
	// Expired ⇒ the admin re-sends; the seat stays occupied (§3.2).
	InviteTokenTTLHours int `yaml:"inviteTokenTtlHours"`
}

// CronValues configures the cron/time-trigger layer (internal/cron). Schedule
// DEFINITIONS live in code (the job registry); their times and on/off flags
// live here, so a misbehaving beat dies with a values change and a restart —
// no code, no migration.
type CronValues struct {
	// TickSeconds is the scheduler sweep period (300 = 5 minutes). Must be ≤
	// the finest job interval; every job's CatchUp must exceed it.
	TickSeconds int `yaml:"tickSeconds"`
	// ClaimStaleSeconds is the takeover threshold: an unfinished occurrence
	// claim older than this is presumed dead and re-run by a later tick.
	ClaimStaleSeconds int `yaml:"claimStaleSeconds"`
	// DefaultZone is the IANA fallback zone for schedule validation and for
	// users with no stored timezone.
	DefaultZone string `yaml:"defaultZone"`
	// Jobs is keyed by job name (the cron.JobName constants). A job absent
	// here — or enabled: false — is dark: the kill switch that disables a
	// misbehaving beat with a values change and a restart.
	Jobs map[string]CronJobValues `yaml:"jobs"`
}

// CronJobValues is one job's schedule knobs. Empty fields keep the code
// default; enabled is the only required decision.
type CronJobValues struct {
	Enabled bool `yaml:"enabled"`
	// At overrides the wall-clock "HH:MM" of AtLocal / AtUserLocal specs.
	At string `yaml:"at"`
	// Zone overrides an AtLocal spec's IANA zone.
	Zone string `yaml:"zone"`
	// Weekdays restricts firing days — cron numbering, 0 = Sunday ("1-5",
	// "4", "1,3,5").
	Weekdays string `yaml:"weekdays"`
	// DayOfMonth restricts an AtLocal spec to one calendar day per month
	// (monthly analytics sources fire on the 1st). 0 keeps the code default.
	DayOfMonth int `yaml:"dayOfMonth"`
	// EverySeconds overrides an Every spec's interval.
	EverySeconds int `yaml:"everySeconds"`
	// MaxUnits caps the per-occurrence fan-out (0 = code default).
	MaxUnits int `yaml:"maxUnits"`
}

type ServerValues struct {
	Port          string `yaml:"port"`
	DefaultDBName string `yaml:"defaultDBName"`
	AWSRegion     string `yaml:"awsRegion"`
	// AllowedHosts restricts which Host headers the server answers. Empty means
	// no restriction (dev/integration). Production lists "api.useindexly.com" so
	// direct-IP requests are rejected. See middleware.HostGuard.
	AllowedHosts []string `yaml:"allowedHosts"`
}

// AdminValues holds operational tuning for the /v1/admin surface. Access is
// governed solely by the DB-backed RBAC roles (middleware.WithAdminAuthorization);
// nothing here grants admin.
type AdminValues struct {
	// CacheRefreshSeconds is the interval at which the plans and roles caches
	// re-load from the DB (StartRefresh), bounding staleness across a fleet.
	CacheRefreshSeconds int `yaml:"cacheRefreshSeconds"`
	// Pagination bounds the admin list endpoints' page sizes.
	Pagination AdminPaginationValues `yaml:"pagination"`
}

// AdminPaginationValues holds the default and maximum page sizes for the admin
// list endpoints. UsersDefault/UsersMax gate HandleAdminListUsers;
// AuditDefault/AuditMax gate HandleAdminListAuditActions.
type AdminPaginationValues struct {
	UsersDefault int `yaml:"usersDefault"`
	UsersMax     int `yaml:"usersMax"`
	AuditDefault int `yaml:"auditDefault"`
	AuditMax     int `yaml:"auditMax"`
}

type AsyncValues struct {
	WorkerCount     int `yaml:"workerCount"`
	LLMWorkerCount  int `yaml:"llmWorkerCount"`
	MaxRetries      int `yaml:"maxRetries"`
	ShutdownSeconds int `yaml:"shutdownSeconds"`
	// TokenLimit is the cumulative LLM token budget (input+output) per
	// TokenWindowSeconds; secondary-queue consumption pauses once it is reached.
	TokenLimit         int `yaml:"tokenLimit"`
	TokenWindowSeconds int `yaml:"tokenWindowSeconds"`
}

type SQSValues struct {
	WaitTimeSeconds   int `yaml:"waitTimeSeconds"`
	VisibilityTimeout int `yaml:"visibilityTimeout"`
	// ClusteringVisibilityTimeout overrides VisibilityTimeout for clustering
	// messages only. Clustering issues a single large LLM call that can outrun
	// the default window; the consumer extends the message's visibility to this
	// value on receipt so it isn't redelivered mid-flight.
	ClusteringVisibilityTimeout int `yaml:"clusteringVisibilityTimeout"`
	MaxMessagesPerBatch         int `yaml:"maxMessagesPerBatch"`
	// GatedPauseSeconds is how long the gated (secondary) SQS consumer sleeps
	// when the token-budget gate is closed before re-checking (backpressure).
	GatedPauseSeconds int `yaml:"gatedPauseSeconds"`
}

type IdempotencyValues struct {
	TTLHours int `yaml:"ttlHours"`
}

type WebhookValues struct {
	MaxClerkBodyBytes              int64 `yaml:"maxClerkBodyBytes"`
	MaxPaddleBodyBytes             int64 `yaml:"maxPaddleBodyBytes"`
	PaddleProcessingTimeoutSeconds int   `yaml:"paddleProcessingTimeoutSeconds"`
}

type PaddleValues struct {
	APICallTimeoutSeconds            int `yaml:"apiCallTimeoutSeconds"`
	PriceCacheTTLSeconds             int `yaml:"priceCacheTTLSeconds"`
	WebhookTimestampToleranceSeconds int `yaml:"webhookTimestampToleranceSeconds"`
}

type S3Values struct {
	PresignPutExpirySeconds       int `yaml:"presignPutExpirySeconds"`
	PresignMultipartExpirySeconds int `yaml:"presignMultipartExpirySeconds"`
	MaxConcurrency                int `yaml:"maxConcurrency"`
}

type LLMValues struct {
	DefaultProvider  string             `yaml:"defaultProvider"`
	DefaultMaxTokens int                `yaml:"defaultMaxTokens"`
	Anthropic        AnthropicLLMValues `yaml:"anthropic"`
	OpenAI           OpenAILLMValues    `yaml:"openai"`
	Gemini           GeminiLLMValues    `yaml:"gemini"`
}

type AnthropicLLMValues struct {
	APIURL            string `yaml:"apiURL"`
	APIVersion        string `yaml:"apiVersion"`
	FallbackModel     string `yaml:"fallbackModel"`
	FallbackMaxTokens int    `yaml:"fallbackMaxTokens"`
	// RequestTimeoutSeconds bounds a single Messages API call. 0 falls back to
	// the provider's built-in default (10 min). Must stay under the SQS
	// clustering visibility override so a slow call can't outlive its lease.
	RequestTimeoutSeconds int `yaml:"requestTimeoutSeconds"`
}

type OpenAILLMValues struct {
	APIURL string `yaml:"apiURL"`
	// FallbackModel is used when a prompt request does not specify a model.
	FallbackModel string `yaml:"fallbackModel"`
}

type GeminiLLMValues struct {
	APIURL string `yaml:"apiURL"`
	// FallbackModel is used when a prompt request does not specify a model.
	FallbackModel string `yaml:"fallbackModel"`
}

// APIValues holds external API base URLs. DataForSEO endpoint URLs are
// intentionally excluded — they stay as package-level consts in the dataForSEO
// provider; only its rendered-fetch tunables live here.
type APIValues struct {
	Tavily     TavilyValues     `yaml:"tavily"`
	YouTube    YouTubeValues    `yaml:"youtube"`
	Sidecar    SidecarValues    `yaml:"sidecar"`
	ImageGen   ImageGenValues   `yaml:"imageGen"`
	HTTPClient HTTPClientValues `yaml:"httpClient"`
	DataForSEO DataForSEOValues `yaml:"dataForSEO"`
	PSI        PSIValues        `yaml:"psi"`
}

// PSIValues points at the Google PageSpeed Insights API (audit engine
// Performance category).
type PSIValues struct {
	RunPagespeedURL string `yaml:"runPagespeedURL"`
}

// DataForSEOValues tunes the OnPage rendered-fetch flow
// (utils.FetchRenderedWebsiteContent). Zero values fall back to the built-in
// defaults in internal/utils, so a missing yaml block can't disable the
// primary onboarding fetch path.
type DataForSEOValues struct {
	// TaskOkStatusCode is the task-level status_code DataForSEO returns on
	// success (their errors ride inside HTTP 200 envelopes).
	TaskOkStatusCode int `yaml:"taskOkStatusCode"`
	// FetchTimeoutSeconds bounds the whole rendered-fetch flow (instant_pages
	// crawl + raw_html collection); on expiry the direct fetch takes over.
	FetchTimeoutSeconds int `yaml:"fetchTimeoutSeconds"`
}

// HTTPClientValues tunes the shared ApiClient http.Client (see apiClient.GetClient).
// The other transport knobs (TLS/dial/idle) stay as consts; only the overall
// request timeout is externalised.
type HTTPClientValues struct {
	TimeoutSeconds int `yaml:"timeoutSeconds"`
}

type TavilyValues struct {
	SearchURL string `yaml:"searchURL"`
}

type YouTubeValues struct {
	SearchURL string `yaml:"searchURL"`
}

// SidecarValues points at the Node sidecar that adapts vendor SDKs (Framer
// Server API today) behind the generic provider/action HTTP contract. The
// timeout must cover a full Framer site build+publish, which is why the
// sidecar publisher gets its own http.Client instead of the shared 60s one.
type SidecarValues struct {
	BaseURL        string `yaml:"baseURL"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
}

type ImageGenValues struct {
	// DefaultProvider selects the active image generation provider ("openai" or
	// "gemini"). Empty defaults to "gemini" for backwards compatibility.
	DefaultProvider string            `yaml:"defaultProvider"`
	Gemini          GeminiImageValues `yaml:"gemini"`
	OpenAI          OpenAIImageValues `yaml:"openai"`
}

type GeminiImageValues struct {
	APIURL                string `yaml:"apiURL"`
	DefaultAspectRatio    string `yaml:"defaultAspectRatio"`
	RequestTimeoutSeconds int    `yaml:"requestTimeoutSeconds"`
}

type OpenAIImageValues struct {
	APIURL                string `yaml:"apiURL"`
	Model                 string `yaml:"model"`
	DefaultAspectRatio    string `yaml:"defaultAspectRatio"`
	RequestTimeoutSeconds int    `yaml:"requestTimeoutSeconds"`
}

type ContentGenerationValues struct {
	ArticleMaxTokens           int    `yaml:"articleMaxTokens"`
	ImageAspectRatio           string `yaml:"imageAspectRatio"`
	MidArticleSectionMaxChars  int    `yaml:"midArticleSectionMaxChars"`
	AltTextMaxChars            int    `yaml:"altTextMaxChars"`
	MetaAssetsMaxTokens        int    `yaml:"metaAssetsMaxTokens"`
	SchemaMaxTokens            int    `yaml:"schemaMaxTokens"`
	MaxCombinedTranscriptChars int    `yaml:"maxCombinedTranscriptChars"`
	MaxTranscriptChars         int    `yaml:"maxTranscriptChars"`
	// SerpFetchDepth is how many organic SERP results the serp-fetch stage pulls
	// per keyword to mine competitor coverage from.
	SerpFetchDepth int `yaml:"serpFetchDepth"`
	// SlugMaxLen caps a generated article's URL slug. Truncation lands on a word
	// boundary, so the emitted slug is usually a little shorter than this.
	SlugMaxLen int `yaml:"slugMaxLen"`
	// ArticleLengthMultiplier scales the average competitor word count into the
	// target word count for a generated article (targetWords = avg × multiplier).
	ArticleLengthMultiplier float64            `yaml:"articleLengthMultiplier"`
	InternalLinks           InternalLinkValues `yaml:"internalLinks"`
}

// InternalLinkValues tunes the best-effort sitemap crawl behind internal-link
// insertion. The stage never fails an article, so these bound cost and latency
// rather than correctness: MaxSitemapURLs caps how many URLs reach the prompt,
// FetchTimeoutSeconds bounds each sitemap HTTP call (the stage uses its own
// client, not the shared 60s ApiClient), and MaxSitemapBytes caps a single
// sitemap document's body.
type InternalLinkValues struct {
	MaxSitemapURLs      int   `yaml:"maxSitemapURLs"`
	FetchTimeoutSeconds int   `yaml:"fetchTimeoutSeconds"`
	MaxSitemapBytes     int64 `yaml:"maxSitemapBytes"`
}

type SchedulingValues struct {
	Weeks int `yaml:"weeks"`
	// UpgradeCadence is the post-upgrade publishing rate (articles/week) applied
	// by ExtendSchedule on the trial→paid upgrade re-run. Must exist in the
	// scheduling engine's cadenceTable.
	UpgradeCadence int `yaml:"upgradeCadence"`
}

type OnboardingValues struct {
	SerpDefaultLanguageCode string `yaml:"serpDefaultLanguageCode"`
	SerpDefaultDepth        int    `yaml:"serpDefaultDepth"`
	// MaxCompetitors caps how many competitors a user may keep on their
	// WebEntity. Enforced server-side in PatchOnboardedUser and surfaced to the
	// onboarding UI via the WebEntity DTO.
	MaxCompetitors int `yaml:"maxCompetitors"`
	// DiscoverCompetitors is how many competitors auto-discovery produces: it is
	// the count asked of the competitor-discovery prompt and the cap applied to
	// the parsed result. Deliberately lower than MaxCompetitors — the LLM seeds
	// the list, the user tops it up to the cap by hand. Falls back to
	// MaxCompetitors when unset; clamped to MaxCompetitors when larger.
	DiscoverCompetitors int `yaml:"discoverCompetitors"`
	// CompetitorSerpDepth is the number of SERP results fetched for the
	// competitor-discovery query — the candidate pool the LLM filters down to
	// DiscoverCompetitors. Independent of SerpDefaultDepth.
	CompetitorSerpDepth int `yaml:"competitorSerpDepth"`
}

// DiscoverCompetitorsOrDefault resolves the auto-discovery competitor count:
// DiscoverCompetitors when set, otherwise MaxCompetitors, never above
// MaxCompetitors (a discovery count over the cap would produce a list
// PatchOnboardedUser then rejects). Zero means "no limit configured" and leaves
// the discovery result untrimmed, matching MaxCompetitors' own semantics.
func (v OnboardingValues) DiscoverCompetitorsOrDefault() int {
	n := v.DiscoverCompetitors
	if n <= 0 {
		n = v.MaxCompetitors
	}
	if v.MaxCompetitors > 0 && n > v.MaxCompetitors {
		n = v.MaxCompetitors
	}
	return n
}

type SiteIntelligenceValues struct {
	UserKeywordsLimit               int `yaml:"userKeywordsLimit"`
	CompetitorKeywordsLimit         int `yaml:"competitorKeywordsLimit"`
	ExpandedKeywordsLimit           int `yaml:"expandedKeywordsLimit"`
	DefaultDomainRating             int `yaml:"defaultDomainRating"`
	FunnelClassificationChunkSize   int `yaml:"funnelClassificationChunkSize"`
	ClusterMaxTokens                int `yaml:"clusterMaxTokens"`
	ManualClusterExamplesPerCluster int `yaml:"manualClusterExamplesPerCluster"`
	ManualClusterMaxTokens          int `yaml:"manualClusterMaxTokens"`
	// SupportingKeywordsPageSize caps how many supporting keywords are returned
	// per cluster in a single API response. The keyword-data payload embeds only
	// the first page of each cluster; the client pages the rest on demand via
	// GetClusterSupportingKeywords.
	SupportingKeywordsPageSize int `yaml:"supportingKeywordsPageSize"`
	// KeywordSearchLimit caps how many hits the keyword-search endpoint returns.
	// Matches are ranked by opportunity score before the cap, and the response
	// reports the pre-cap total so the client can say the list is partial.
	KeywordSearchLimit int                    `yaml:"keywordSearchLimit"`
	KeywordFilters     KeywordFilterValues    `yaml:"keywordFilters"`
	RefreshCandidate   RefreshCandidateValues `yaml:"refreshCandidate"`
	Manual             ManualKeywordValues    `yaml:"manual"`
	Scoring            ScoringValues          `yaml:"scoring"`
	Trial              SIETrialValues         `yaml:"trial"`
	// Strategies holds the per-SEO-strategy overrides, keyed by strategy id
	// (models.SEOStrategyBalancedGrowth etc.). The top-level KeywordFilters and
	// Scoring above are the balanced_growth defaults; a strategy absent from
	// this map runs on them unchanged, so balanced_growth needs no entry.
	Strategies map[string]SIEStrategyValues `yaml:"strategies"`
}

// SIEStrategyValues is one strategy's deviation from the default SIE tuning.
// KeywordFilters replaces the whole filter set (the filters are the real
// strategy change — they bound what DataForSEO returns); Scoring overrides
// individual knobs on top of the base ScoringValues so a strategy entry can't
// accidentally zero a knob it didn't mean to touch.
type SIEStrategyValues struct {
	KeywordFilters *KeywordFilterValues   `yaml:"keywordFilters"`
	Scoring        *ScoringOverrideValues `yaml:"scoring"`
}

// ScoringOverrideValues are the per-strategy scoring knobs. Pointer fields:
// nil inherits the base value. Only the knobs a strategy actually tunes are
// exposed here — add fields as future strategies need them rather than
// duplicating the full ScoringValues per strategy in YAML.
type ScoringOverrideValues struct {
	VolumeLogAnchor  *float64 `yaml:"volumeLogAnchor"`
	WeightVolume     *float64 `yaml:"weightVolume"`
	WeightDifficulty *float64 `yaml:"weightDifficulty"`
	WeightFunnel     *float64 `yaml:"weightFunnel"`
}

// FiltersForStrategy resolves the keyword-filter set for a strategy id,
// falling back to the top-level (balanced_growth) filters when the strategy
// has no entry or no filter override.
func (v SiteIntelligenceValues) FiltersForStrategy(strategy string) KeywordFilterValues {
	if sv, ok := v.Strategies[strategy]; ok && sv.KeywordFilters != nil {
		return *sv.KeywordFilters
	}
	return v.KeywordFilters
}

// ScoringForStrategy resolves the scoring knobs for a strategy id: the base
// ScoringValues with the strategy's overrides applied on top.
func (v SiteIntelligenceValues) ScoringForStrategy(strategy string) ScoringValues {
	sc := v.Scoring
	sv, ok := v.Strategies[strategy]
	if !ok || sv.Scoring == nil {
		return sc
	}
	o := sv.Scoring
	if o.VolumeLogAnchor != nil {
		sc.VolumeLogAnchor = *o.VolumeLogAnchor
	}
	if o.WeightVolume != nil {
		sc.WeightVolume = *o.WeightVolume
	}
	if o.WeightDifficulty != nil {
		sc.WeightDifficulty = *o.WeightDifficulty
	}
	if o.WeightFunnel != nil {
		sc.WeightFunnel = *o.WeightFunnel
	}
	return sc
}

// SIETrialValues sizes the trial-mode SIE pipeline (WebEntityContext
// sie_mode == "trial"). Every limit here shadows a full-mode counterpart:
// the fetch limits shadow the top-level siteIntelligence limits,
// scheduleWeeks/cadence shadow scheduling.weeks and the WebEntity's
// publishing cadence. MaxPersistedKeywords and MaxArticles have no full-mode
// counterpart — full mode persists everything and generation is uncapped.
type SIETrialValues struct {
	UserKeywordsLimit       int `yaml:"userKeywordsLimit"`
	CompetitorKeywordsLimit int `yaml:"competitorKeywordsLimit"`
	ExpandedKeywordsLimit   int `yaml:"expandedKeywordsLimit"`
	// MaxPersistedKeywords caps how many post-filter keywords survive into the
	// keyword collection (sorted volume desc, CPC desc before the cut).
	MaxPersistedKeywords int `yaml:"maxPersistedKeywords"`
	// ClusterCount is rendered into the clustering prompt as an exact count
	// (full mode keeps the 6–10 range).
	ClusterCount  int `yaml:"clusterCount"`
	ScheduleWeeks int `yaml:"scheduleWeeks"`
	// Cadence must exist in the scheduling engine's cadenceTable.
	Cadence int `yaml:"cadence"`
	// MaxArticles caps CGE generations started per trial user (402
	// trial_limit_reached past it). Retries of a failed generation are exempt.
	MaxArticles int `yaml:"maxArticles"`
}

// KeywordFilterValues gates which keywords enter the SIE pipeline at all — they
// are applied as DataForSEO request filters on the ranked_keywords and
// keyword_ideas calls, so they bound the raw candidate set before any scoring or
// clustering runs. Tightening them shrinks the pipeline's input; loosening them
// admits more (and noisier) keywords.
//
// The difficulty band is relative to the user's domain rating (udr):
// min = max(DifficultyFloor, udr − DifficultyBelowDR), max = udr + DifficultyAboveDR
// — unless DifficultyMaxAbsolute is set, which pins a fixed band instead.
// DifficultyBelowDR is the same domain-rating anchor as Scoring.DifficultyDRAnchor.
type KeywordFilterValues struct {
	MaxRankPosition int `yaml:"maxRankPosition"`
	MinSearchVolume int `yaml:"minSearchVolume"`
	// MaxSearchVolume caps search volume (<=) when > 0; 0 means no upper
	// bound. Strategies chasing early wins use it to skip head terms.
	MaxSearchVolume   int `yaml:"maxSearchVolume"`
	DifficultyFloor   int `yaml:"difficultyFloor"`
	DifficultyBelowDR int `yaml:"difficultyBelowDR"`
	DifficultyAboveDR int `yaml:"difficultyAboveDR"`
	// DifficultyMaxAbsolute, when > 0, replaces the udr-relative difficulty
	// band with the fixed window [DifficultyFloor, DifficultyMaxAbsolute] —
	// the early_footholds strategy caps KD regardless of domain rating.
	DifficultyMaxAbsolute int `yaml:"difficultyMaxAbsolute"`
}

// RefreshCandidateValues is the "striking distance" SERP band: keywords already
// ranking within [MinPosition, MaxPosition] are flagged "refresh_candidate" in
// post-processing, marking them as cheap wins to re-optimise rather than net-new
// articles to write.
type RefreshCandidateValues struct {
	MinPosition int `yaml:"minPosition"`
	MaxPosition int `yaml:"maxPosition"`
}

type ManualKeywordValues struct {
	HardcodedUsed  int `yaml:"hardcodedUsed"`
	HardcodedLimit int `yaml:"hardcodedLimit"`
	// Suggestion fallback knobs (used when a typed keyword has no reliable
	// DataForSEO data). SuggestionsAPILimit caps the rows each fallback endpoint
	// returns; SuggestionsKeep is how many survive filtering and reach the UI;
	// SuggestionsMinResults is the threshold below which keyword_suggestions is
	// considered too thin and related_keywords is also consulted;
	// RelatedKeywordsDepth is the related-keywords discovery depth.
	SuggestionsAPILimit   int `yaml:"suggestionsApiLimit"`
	SuggestionsKeep       int `yaml:"suggestionsKeep"`
	SuggestionsMinResults int `yaml:"suggestionsMinResults"`
	RelatedKeywordsDepth  int `yaml:"relatedKeywordsDepth"`
}

// ScoringValues holds the opportunity-score weights and anchors. All floats so
// the score math matches the pre-externalization consts bit-for-bit.
type ScoringValues struct {
	VolumeLogAnchor      float64 `yaml:"volumeLogAnchor"`
	VolumeScoreCap       float64 `yaml:"volumeScoreCap"`
	VolumeScoreFloor     float64 `yaml:"volumeScoreFloor"`
	IntentScoreBOFU      float64 `yaml:"intentScoreBOFU"`
	IntentScoreMOFU      float64 `yaml:"intentScoreMOFU"`
	IntentScoreTOFU      float64 `yaml:"intentScoreTOFU"`
	IntentScoreDefault   float64 `yaml:"intentScoreDefault"`
	DifficultyScoreNull  float64 `yaml:"difficultyScoreNull"`
	DifficultyScoreFloor float64 `yaml:"difficultyScoreFloor"`
	DifficultyScoreCap   float64 `yaml:"difficultyScoreCap"`
	DifficultyKDScale    float64 `yaml:"difficultyKDScale"`
	// DifficultyDRAnchor/DifficultyDRSlope make difficulty relative to the
	// user's domain rating: adjustedKD = kd − (userDR − anchor) × slope. A
	// domain at the anchor sees raw KD; stronger domains see a discounted KD,
	// weaker ones a penalty.
	DifficultyDRAnchor float64 `yaml:"difficultyDRAnchor"`
	DifficultyDRSlope  float64 `yaml:"difficultyDRSlope"`
	WeightVolume       float64 `yaml:"weightVolume"`
	WeightDifficulty   float64 `yaml:"weightDifficulty"`
	WeightFunnel       float64 `yaml:"weightFunnel"`
	OpportunityScale   float64 `yaml:"opportunityScale"`
	// OpportunityGamma is the exponent of the concave curve applied to the raw
	// weighted score: final = scale × (raw/scale)^gamma. Gamma < 1 lifts
	// low/mid scores while keeping the 0 and scale endpoints fixed; 1.0 is a
	// no-op (the pre-curve behavior).
	OpportunityGamma float64 `yaml:"opportunityGamma"`
	ScorePrecision   float64 `yaml:"scorePrecision"`
}

// LoadValues reads the runtime values YAML into c.Values. Resolution order:
//  1. VALUES_FILE env var, if set (explicit override).
//  2. values/<ENVIRONMENT>/values.yaml — which must exist.
//
// Requires LoadEnvConfig to have run first so c.Env.Environment is populated.
// An environment with no matching values file is fatal: there is no fallback,
// because silently substituting another environment's file (e.g. production)
// would change runtime semantics — notably leaking production's Host allowlist
// into an unrestricted environment. A missing or unparseable file is fatal for
// the same reason.
func LoadValues(c *AppConfig) error {
	path, err := resolveValuesPath(c.Env.Environment)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read values file %q: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &c.Values); err != nil {
		return fmt.Errorf("parse values file %q: %w", path, err)
	}

	log.Info("loaded runtime values", "path", path, "environment", c.Env.Environment)
	return nil
}

// resolveValuesPath picks the values file per the resolution order documented
// on LoadValues. An empty environment, or one whose values file is absent, is a
// configuration error rather than grounds for a fallback.
func resolveValuesPath(environment string) (string, error) {
	if explicit := os.Getenv("VALUES_FILE"); explicit != "" {
		return explicit, nil
	}

	if environment == "" {
		return "", fmt.Errorf("cannot resolve values file: ENVIRONMENT is empty")
	}

	envPath := filepath.Join("values", environment, "values.yaml")
	if _, err := os.Stat(envPath); err != nil {
		return "", fmt.Errorf("no values file for environment %q (expected %s): %w", environment, envPath, err)
	}
	return envPath, nil
}
