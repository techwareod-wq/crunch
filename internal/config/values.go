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
	// WarehouseHub holds the WarehouseHub feature modules' tunables. Each
	// module owns one sub-block.
	WarehouseHub WarehouseHubValues `yaml:"warehousehub"`
}

// WarehouseHubValues is the values `warehousehub:` block. Module sub-blocks
// are added here as each module lands.
type WarehouseHubValues struct {
	// PublicBaseURL is the public site origin (listing links, JSON-LD,
	// sitemap). Empty until D-005 (domain) is resolved.
	PublicBaseURL string `yaml:"publicBaseURL"`
	// AdminBaseURL is the admin panel origin (links in staff emails).
	AdminBaseURL string `yaml:"adminBaseURL"`
	// Attributes tunes the attribute engine (spec 02).
	Attributes AttributesValues `yaml:"attributes"`
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
	LLMWorkerCount  int `yaml:"llmWorkerCount"`
	MaxRetries      int `yaml:"maxRetries"`
	ShutdownSeconds int `yaml:"shutdownSeconds"`
	// TokenLimit is the cumulative LLM token budget (input+output) per
	// TokenWindowSeconds; secondary-queue consumption pauses once it is reached.
	TokenLimit         int `yaml:"tokenLimit"`
	TokenWindowSeconds int `yaml:"tokenWindowSeconds"`
}

type SQSValues struct {
	WaitTimeSeconds     int `yaml:"waitTimeSeconds"`
	VisibilityTimeout   int `yaml:"visibilityTimeout"`
	MaxMessagesPerBatch int `yaml:"maxMessagesPerBatch"`
	// GatedPauseSeconds is how long the gated (secondary) SQS consumer sleeps
	// when the token-budget gate is closed before re-checking (backpressure).
	GatedPauseSeconds int `yaml:"gatedPauseSeconds"`
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

// APIValues holds external API base URLs and client tunables.
type APIValues struct {
	ImageGen   ImageGenValues   `yaml:"imageGen"`
	HTTPClient HTTPClientValues `yaml:"httpClient"`
}

// HTTPClientValues tunes the shared ApiClient http.Client (see apiClient.GetClient).
// The other transport knobs (TLS/dial/idle) stay as consts; only the overall
// request timeout is externalised.
type HTTPClientValues struct {
	TimeoutSeconds int `yaml:"timeoutSeconds"`
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
