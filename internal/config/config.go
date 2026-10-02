package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// maxSQSBatchSize is the hard ceiling AWS imposes on ReceiveMessage's
// MaxNumberOfMessages. Values above this are rejected per-call, which stalls
// the consumer, so the batch size is clamped here rather than trusted blindly.
const maxSQSBatchSize = 10

const (
	// defaultEnvironment is the only baked-in default left: it is needed before
	// the values file is loaded (it selects which values/<env>/values.yaml to
	// read). It must name an environment that has a values file, since an
	// unmatched environment is now fatal (see LoadValues). All other operational
	// defaults now live in values.yaml.
	defaultEnvironment = "integration"

	envProduction  = "production"
	envDevelopment = "development"
)

type DatabaseConfig struct {
	URL  string
	Name string
}

type ServerConfig struct {
	Port string
	// AllowedOrigins is the CORS allowlist. Empty means permissive ("*"),
	// preserving the prior default for local/dev.
	AllowedOrigins []string
	// AllowedHosts is the Host-header allowlist. Empty means no restriction;
	// when set, requests whose Host is not listed are rejected (see
	// middleware.HostGuard). Production lists the public API host.
	AllowedHosts []string
}

type ClerkConfig struct {
	SecretKey     string
	WebhookSecret string
}

type EnvConfig struct {
	Environment string
}

type AWSConfig struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// PublicBucket holds public media (listing photos, public docs). It is
	// readable only through CloudFront (OAC bucket policy) — never via ACLs
	// (D-015).
	PublicBucket string
	// PrivateBucket holds staff-only docs; served by short-lived presigned
	// GETs only. No public access.
	PrivateBucket string
	// PublicMediaBaseURL is the CloudFront origin in front of PublicBucket,
	// e.g. "https://media.example.com". Public media URLs = base + "/" + key.
	PublicMediaBaseURL string
}

// MapsConfig holds the Google Maps Platform key (geocoding, D-061/D-078).
// Empty disables geocoding: drafts save without a pin and editors set it by
// hand.
type MapsConfig struct {
	GoogleAPIKey string
}

type LLMConfig struct {
	AnthropicAPIKey string
	// VoyageAPIKey enables embeddings (AI search similar matches, D-082).
	VoyageAPIKey string
	OpenAIAPIKey string
	GeminiAPIKey string
}

type SQSConfig struct {
	QueueURL            string
	SecondaryQueueURL   string
	DLQUrl              string
	WaitTimeSeconds     int
	VisibilityTimeout   int
	MaxMessagesPerBatch int
}

type AsyncHandlerConfig struct {
	WorkerCount     int
	LLMWorkerCount  int
	MaxRetries      int
	ShutdownTimeout time.Duration
	TokenLimit      int
	TokenWindow     time.Duration
}

type AppConfig struct {
	Database     DatabaseConfig
	Server       ServerConfig
	Env          EnvConfig
	Clerk        ClerkConfig
	AWS          AWSConfig
	LLM          LLMConfig
	Maps         MapsConfig
	SQS          SQSConfig
	AsyncHandler AsyncHandlerConfig
	// Values holds the YAML-sourced runtime tunables + external API URLs. The
	// loaders below read it as their default layer (env vars still override).
	Values Values
}

func (c *AppConfig) LoadDatabaseConfig() {
	c.Database.URL = os.Getenv("MONGO_URI")
	c.Database.Name = os.Getenv("MONGO_DB_NAME")
	if c.Database.Name == "" {
		c.Database.Name = c.Values.Server.DefaultDBName
	}
}

func (c *AppConfig) LoadServerConfig() {
	c.Server.Port = os.Getenv("PORT")
	if c.Server.Port == "" {
		c.Server.Port = c.Values.Server.Port
	}
	c.Server.AllowedOrigins = parseCSV(os.Getenv("CORS_ALLOWED_ORIGINS"))
	// Host allowlist: env override (ALLOWED_HOSTS, CSV) falls back to the
	// per-environment values.yaml. Empty leaves the server unrestricted.
	c.Server.AllowedHosts = c.Values.Server.AllowedHosts
	if envHosts := parseCSV(os.Getenv("ALLOWED_HOSTS")); len(envHosts) > 0 {
		c.Server.AllowedHosts = envHosts
	}
}

// LoadSiteConfig lets the deploy set the public site / admin panel origins
// (PUBLIC_BASE_URL, ADMIN_BASE_URL) over the values.yaml defaults. They feed
// listing links in the enquiry inbox and the listing JSON-LD url.
func (c *AppConfig) LoadSiteConfig() {
	if v := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")); v != "" {
		c.Values.WarehouseHub.PublicBaseURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("ADMIN_BASE_URL")); v != "" {
		c.Values.WarehouseHub.AdminBaseURL = strings.TrimRight(v, "/")
	}
}

// atoiEnv reads an integer env override. An unset (empty) var returns 0 so the
// caller falls back to its YAML default. A set-but-malformed value is logged
// and also treated as 0 (fall back to the default) rather than silently
// swallowed, so a typo like ASYNC_WORKER_COUNT=ten surfaces in the logs.
func atoiEnv(name string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Warn("malformed integer env override, using default", "var", name, "value", raw)
		return 0
	}
	return n
}

// parseCSV splits a comma-separated env value into trimmed, non-empty entries.
func parseCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (c *AppConfig) LoadEnvConfig() {
	c.Env.Environment = os.Getenv("ENVIRONMENT")
	if c.Env.Environment == "" {
		c.Env.Environment = defaultEnvironment
	}
}

func (c *AppConfig) LoadClerkConfig() {
	c.Clerk.SecretKey = os.Getenv("CLERK_SECRET_KEY")
	c.Clerk.WebhookSecret = os.Getenv("CLERK_WEBHOOK_SECRET")
}

func (c *AppConfig) LoadAWSConfig() {
	c.AWS.Region = os.Getenv("AWS_REGION")
	if c.AWS.Region == "" {
		c.AWS.Region = c.Values.Server.AWSRegion
	}
	c.AWS.AccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	c.AWS.SecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	c.AWS.PublicBucket = os.Getenv("AWS_S3_PUBLIC_BUCKET")
	c.AWS.PrivateBucket = os.Getenv("AWS_S3_PRIVATE_BUCKET")
	c.AWS.PublicMediaBaseURL = strings.TrimRight(os.Getenv("PUBLIC_MEDIA_BASE_URL"), "/")
}

func (c *EnvConfig) IsProd() bool {
	return c.Environment == envProduction
}

func (c *EnvConfig) IsDev() bool {
	return c.Environment == envDevelopment
}

func (c *AppConfig) LoadLLMConfig() {
	c.LLM.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	c.LLM.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	c.LLM.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	c.LLM.VoyageAPIKey = os.Getenv("VOYAGE_API_KEY")
}

func (c *AppConfig) LoadMapsConfig() {
	c.Maps.GoogleAPIKey = os.Getenv("GOOGLE_MAPS_API_KEY")
}

func (c *AppConfig) LoadSQSConfig() {
	c.SQS.QueueURL = os.Getenv("SQS_QUEUE_URL")
	c.SQS.SecondaryQueueURL = os.Getenv("SQS_SECONDARY_QUEUE_URL")
	c.SQS.DLQUrl = os.Getenv("SQS_DLQ_URL")

	waitTime := atoiEnv("SQS_WAIT_TIME_SECONDS")
	if waitTime == 0 {
		waitTime = c.Values.SQS.WaitTimeSeconds
	}
	c.SQS.WaitTimeSeconds = waitTime

	visTimeout := atoiEnv("SQS_VISIBILITY_TIMEOUT")
	if visTimeout == 0 {
		visTimeout = c.Values.SQS.VisibilityTimeout
	}
	c.SQS.VisibilityTimeout = visTimeout

	// Batch size has no env var — it is a pure YAML tunable.
	batch := c.Values.SQS.MaxMessagesPerBatch
	if batch > maxSQSBatchSize {
		log.Warn("sqs.maxMessagesPerBatch exceeds AWS limit, clamping",
			"configured", batch,
			"clampedTo", maxSQSBatchSize,
		)
		batch = maxSQSBatchSize
	}
	c.SQS.MaxMessagesPerBatch = batch
}

func (c *AppConfig) LoadAsyncHandlerConfig() {
	workerCount := atoiEnv("ASYNC_WORKER_COUNT")
	if workerCount == 0 {
		workerCount = c.Values.Async.WorkerCount
	}
	c.AsyncHandler.WorkerCount = workerCount

	llmWorkerCount := atoiEnv("ASYNC_LLM_WORKER_COUNT")
	if llmWorkerCount == 0 {
		llmWorkerCount = c.Values.Async.LLMWorkerCount
	}
	c.AsyncHandler.LLMWorkerCount = llmWorkerCount

	maxRetries := atoiEnv("ASYNC_MAX_RETRIES")
	if maxRetries == 0 {
		maxRetries = c.Values.Async.MaxRetries
	}
	c.AsyncHandler.MaxRetries = maxRetries

	shutdownSec := atoiEnv("ASYNC_SHUTDOWN_TIMEOUT_SECONDS")
	if shutdownSec == 0 {
		shutdownSec = c.Values.Async.ShutdownSeconds
	}
	c.AsyncHandler.ShutdownTimeout = time.Duration(shutdownSec) * time.Second

	tokenLimit := atoiEnv("ASYNC_TOKEN_LIMIT")
	if tokenLimit == 0 {
		tokenLimit = c.Values.Async.TokenLimit
	}
	c.AsyncHandler.TokenLimit = tokenLimit

	tokenWindowSec := atoiEnv("ASYNC_TOKEN_WINDOW_SECONDS")
	if tokenWindowSec == 0 {
		tokenWindowSec = c.Values.Async.TokenWindowSeconds
	}
	c.AsyncHandler.TokenWindow = time.Duration(tokenWindowSec) * time.Second
}

func LoadConfigFromEnv(c *AppConfig) error {
	_ = godotenv.Load()
	// Environment must be resolved first: it selects which values/<env>/values.yaml
	// LoadValues reads. Every loader below then consumes c.Values as its default.
	c.LoadEnvConfig()
	if err := LoadValues(c); err != nil {
		return err
	}
	c.LoadDatabaseConfig()
	c.LoadServerConfig()
	c.LoadSiteConfig()
	c.LoadClerkConfig()
	c.LoadAWSConfig()
	c.LoadLLMConfig()
	c.LoadMapsConfig()
	c.LoadSQSConfig()
	c.LoadAsyncHandlerConfig()
	return nil
}
