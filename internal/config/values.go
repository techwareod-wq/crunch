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
type Values struct {
	Server      ServerValues      `yaml:"server"`
	Admin       AdminValues       `yaml:"admin"`
	Async       AsyncValues       `yaml:"async"`
	SQS         SQSValues         `yaml:"sqs"`
	Idempotency IdempotencyValues `yaml:"idempotency"`
	Webhooks    WebhookValues     `yaml:"webhooks"`
	S3          S3Values          `yaml:"s3"`
	Storage     StorageValues     `yaml:"storage"`
	LLM         LLMValues         `yaml:"llm"`
	APIs        APIValues         `yaml:"apis"`
	Cron        CronValues        `yaml:"cron"`
	// WarehouseHub holds the WarehouseHub services' tunables. Each
	// service owns one sub-block.
	WarehouseHub WarehouseHubValues `yaml:"warehousehub"`
}

// WarehouseHubValues is the values `warehousehub:` block. Service sub-blocks
// are added here as each service lands.
type WarehouseHubValues struct {
	// PublicBaseURL is the public site origin (listing links, JSON-LD,
	// sitemap). Empty until D-005 (domain) is resolved.
	PublicBaseURL string `yaml:"publicBaseURL"`
	// AdminBaseURL is the admin panel origin (links in staff emails).
	AdminBaseURL string `yaml:"adminBaseURL"`
	// Attributes tunes the attribute engine (spec 02).
	Attributes AttributesValues `yaml:"attributes"`
	// Catalog tunes listings, review and media (spec 03).
	Catalog CatalogValues `yaml:"catalog"`
	// Search tunes structured search and the map (spec 04).
	Search SearchValues `yaml:"search"`
	// AISearch tunes natural-language search (spec 05).
	AISearch AISearchValues `yaml:"aisearch"`
	// Enquiries tunes the enquiry form and inbox (spec 06).
	Enquiries EnquiriesValues `yaml:"enquiries"`
	// Analytics tunes search analytics (spec 07).
	Analytics AnalyticsValues `yaml:"analytics"`
}

// EnquiriesValues is the `warehousehub.enquiries` block.
type EnquiriesValues struct {
	// MessageMaxLen caps the free-text requirement (D-102), in characters.
	MessageMaxLen int `yaml:"messageMaxLen"`
	// ListDefault / ListMax bound the inbox page sizes.
	ListDefault int `yaml:"listDefault"`
	ListMax     int `yaml:"listMax"`
	// ExportMax caps the rows of one CSV export.
	ExportMax int `yaml:"exportMax"`
}

// AnalyticsValues is the `warehousehub.analytics` block.
type AnalyticsValues struct {
	// Zone is the IANA zone whose calendar days the rollups and dashboards
	// use.
	Zone string `yaml:"zone"`
	// TopDefault / TopMax bound the top / zero-result query lists.
	TopDefault int `yaml:"topDefault"`
	TopMax     int `yaml:"topMax"`
	// LogDefault / LogMax bound the raw log page sizes.
	LogDefault int `yaml:"logDefault"`
	LogMax     int `yaml:"logMax"`
}

// AISearchValues is the `warehousehub.aisearch` block.
type AISearchValues struct {
	// Enabled is the AI search feature flag. Off: the AI routes aren't
	// registered (404), approve queues no embed jobs and queued ones no-op.
	// Structured search is unaffected.
	Enabled bool `yaml:"enabled"`
	// Model is the extraction model (D-080).
	Model string `yaml:"model"`
	// MaxTokens caps the set_filters tool call.
	MaxTokens int `yaml:"maxTokens"`
	// LLMDeadlineMillis is the hard deadline on the LLM call; past it the
	// search runs on the regex pre-parse (D-088).
	LLMDeadlineMillis int `yaml:"llmDeadlineMillis"`
	// MaxQueryChars truncates the query.
	MaxQueryChars int `yaml:"maxQueryChars"`
	// FallbackThreshold: fewer results than this (after radius expansion)
	// adds similar matches (D-084).
	FallbackThreshold int `yaml:"fallbackThreshold"`
	// FallbackLimit caps the similar matches returned.
	FallbackLimit int `yaml:"fallbackLimit"`
	// Embeddings (D-082): Voyage endpoint, model and output dimension. A
	// model change re-embeds on the next reembed-all.
	VoyageURL          string `yaml:"voyageURL"`
	EmbedModel         string `yaml:"embedModel"`
	EmbedDims          int    `yaml:"embedDims"`
	EmbedTimeoutMillis int    `yaml:"embedTimeoutMillis"`
	// Atlas Vector Search (D-084): index name and candidate pool.
	VectorIndex         string `yaml:"vectorIndex"`
	VectorNumCandidates int    `yaml:"vectorNumCandidates"`
}

// SearchValues is the `warehousehub.search` block.
type SearchValues struct {
	// DefaultRadiusKm per country (D-070); "default" applies elsewhere.
	DefaultRadiusKm map[string]int `yaml:"defaultRadiusKm"`
	// RadiusSteps are the expansion rings in km, ascending (D-071). The
	// last step is the search's maximum reach.
	RadiusSteps []int `yaml:"radiusSteps"`
	// MinResults stops the expansion once a ring holds this many (D-071).
	MinResults int `yaml:"minResults"`
	// PageLimitDefault / PageLimitMax bound one results page.
	PageLimitDefault int `yaml:"pageLimitDefault"`
	PageLimitMax     int `yaml:"pageLimitMax"`
	// Currency per country for price filters (no FX, D-058).
	Currency map[string]string `yaml:"currency"`
	// GeocodeCacheHours is how long a resolved location is reused (D-078;
	// verify Google's caching terms before launch).
	GeocodeCacheHours int `yaml:"geocodeCacheHours"`
	// Geocoder circuit breaker: open after BreakerFailures consecutive
	// failures, for BreakerOpenSeconds.
	BreakerFailures    int `yaml:"breakerFailures"`
	BreakerOpenSeconds int `yaml:"breakerOpenSeconds"`
}

// RadiusFor returns the default radius for country.
func (v SearchValues) RadiusFor(country string) int {
	if r, ok := v.DefaultRadiusKm[country]; ok && r > 0 {
		return r
	}
	if r := v.DefaultRadiusKm["default"]; r > 0 {
		return r
	}
	return 25
}

// CurrencyFor returns the price currency for country.
func (v SearchValues) CurrencyFor(country string) string {
	if c := v.Currency[country]; c != "" {
		return c
	}
	return "INR"
}

// CatalogValues is the `warehousehub.catalog` block.
type CatalogValues struct {
	// MaxPhotos caps a warehouse's photos (D-062).
	MaxPhotos int `yaml:"maxPhotos"`
	// MaxPhotoBytes / MaxDocBytes cap one upload (D-062).
	MaxPhotoBytes int64 `yaml:"maxPhotoBytes"`
	MaxDocBytes   int64 `yaml:"maxDocBytes"`
	// MediaGcPendingHours: an upload never confirmed within this is deleted.
	MediaGcPendingHours int `yaml:"mediaGcPendingHours"`
	// MediaGcUnreferencedDays: media no revision references for this long
	// is deleted.
	MediaGcUnreferencedDays int `yaml:"mediaGcUnreferencedDays"`
	// ListDefault / ListMax bound the admin list page sizes.
	ListDefault int `yaml:"listDefault"`
	ListMax     int `yaml:"listMax"`
	// PublicPageSize is the page size of the public slugs / sitemap feeds.
	PublicPageSize int `yaml:"publicPageSize"`
	// BulkApproveMax caps one bulk-approve request.
	BulkApproveMax int `yaml:"bulkApproveMax"`
}

// AttributesValues is the `warehousehub.attributes` block.
type AttributesValues struct {
	// CacheRefreshSeconds bounds how stale another instance's rules snapshot
	// can be after a tree/industry write (the writer reloads at once).
	CacheRefreshSeconds int `yaml:"cacheRefreshSeconds"`
	// RecomputeBatchSize is how many warehouses one recompute_batch message
	// evaluates.
	RecomputeBatchSize int `yaml:"recomputeBatchSize"`
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
	// (e.g. a monthly job firing on the 1st). 0 keeps the code default.
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
	// no restriction (dev/integration). Production lists the public API host so
	// direct-IP requests are rejected. See middleware.HostGuard.
	AllowedHosts []string `yaml:"allowedHosts"`
}

// AdminValues holds operational tuning for the /v1/admin surface. Access
// comes only from a user's role + permissions (internal/authz).
type AdminValues struct {
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
	MaxRetries      int `yaml:"maxRetries"`
	ShutdownSeconds int `yaml:"shutdownSeconds"`
}

type SQSValues struct {
	WaitTimeSeconds     int `yaml:"waitTimeSeconds"`
	VisibilityTimeout   int `yaml:"visibilityTimeout"`
	MaxMessagesPerBatch int `yaml:"maxMessagesPerBatch"`
}

type IdempotencyValues struct {
	TTLHours int `yaml:"ttlHours"`
}

type WebhookValues struct {
	MaxClerkBodyBytes int64 `yaml:"maxClerkBodyBytes"`
}

type S3Values struct {
	PresignPutExpirySeconds       int `yaml:"presignPutExpirySeconds"`
	PresignMultipartExpirySeconds int `yaml:"presignMultipartExpirySeconds"`
	MaxConcurrency                int `yaml:"maxConcurrency"`
}

// StorageValues holds the media link lifetimes (D-015, D-062).
type StorageValues struct {
	// PrivateLinkSeconds is the presigned GET lifetime for staff-only docs
	// in the private bucket (5 min).
	PrivateLinkSeconds int `yaml:"privateLinkSeconds"`
	// UploadLinkSeconds is the presigned PUT lifetime for media uploads
	// (15 min).
	UploadLinkSeconds int `yaml:"uploadLinkSeconds"`
}

type LLMValues struct {
	Anthropic AnthropicLLMValues `yaml:"anthropic"`
}

type AnthropicLLMValues struct {
	APIURL     string `yaml:"apiURL"`
	APIVersion string `yaml:"apiVersion"`
	// RequestTimeoutSeconds bounds a single Messages API call. 0 falls back to
	// the provider's built-in default (10 min). Must stay under the SQS
	// clustering visibility override so a slow call can't outlive its lease.
	RequestTimeoutSeconds int `yaml:"requestTimeoutSeconds"`
}

// APIValues holds external API base URLs and client tunables.
type APIValues struct {
	HTTPClient HTTPClientValues `yaml:"httpClient"`
	Geocode    GeocodeValues    `yaml:"geocode"`
}

// GeocodeValues configures the Google geocoder (D-061, D-078).
type GeocodeValues struct {
	// APIURL is the Geocoding API endpoint.
	APIURL string `yaml:"apiURL"`
	// TimeoutMillis bounds one geocode call (800 ms, D-078).
	TimeoutMillis int `yaml:"timeoutMillis"`
}

// HTTPClientValues tunes the shared ApiClient http.Client (see apiClient.GetClient).
// The other transport knobs (TLS/dial/idle) stay as consts; only the overall
// request timeout is externalised.
type HTTPClientValues struct {
	TimeoutSeconds int `yaml:"timeoutSeconds"`
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
