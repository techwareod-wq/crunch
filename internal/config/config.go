package config

import (
	"encoding/base64"
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

	paddleEnvSandbox    = "sandbox"
	paddleEnvProduction = "production"
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
	// middleware.HostGuard). Production lists "api.useindexly.com".
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
	S3Bucket        string
}

type LLMConfig struct {
	OpenAIAPIKey    string
	AnthropicAPIKey string
	GeminiAPIKey    string
	DefaultProvider string
}

type dataForSeoConfigs struct {
	Username string
	Password string
}

type SQSConfig struct {
	QueueURL                    string
	SecondaryQueueURL           string
	DLQUrl                      string
	WaitTimeSeconds             int
	VisibilityTimeout           int
	ClusteringVisibilityTimeout int
	MaxMessagesPerBatch         int
}

type AsyncHandlerConfig struct {
	WorkerCount     int
	LLMWorkerCount  int
	MaxRetries      int
	ShutdownTimeout time.Duration
	TokenLimit      int
	TokenWindow     time.Duration
}

type TavilyConfig struct {
	APIKey string
}

type YouTubeConfig struct {
	APIKey           string
	WebshareUsername string
	WebsharePassword string
}

type PaddleConfig struct {
	APIKey        string // PADDLE_API_KEY
	WebhookSecret string // PADDLE_WEBHOOK_SECRET (notification destination secret, "pdl_ntfset_...")
	Environment   string // derived from ENVIRONMENT (see EnvConfig.PaddleEnvironment): "sandbox" | "production"
	ProductID     string // PADDLE_PRODUCT_ID — filter for ListPrices so only our product's plans surface
	// TeamProductID is the per-seat "Indexly Team" product. EMPTY IS A
	// SUPPORTED STEADY STATE (the feature ships dark before the Paddle product
	// exists): no boot validation, no warning spam — the company checkout
	// guard is the single enforcement point and 400s cleanly while unset.
	TeamProductID string // PADDLE_TEAM_PRODUCT_ID
}

type SidecarConfig struct {
	// SharedSecret authenticates central to the Node sidecar (X-Sidecar-Token
	// header). The sidecar refuses to boot without its copy, so an empty value
	// here means every publish fails with UNAUTHORIZED.
	SharedSecret string // SIDECAR_SHARED_SECRET
}

// GSCConfig holds the Google Search Console service-account credential.
// Unlike DataForSEO this is deliberately optional: an empty key means the GSC
// provider stays nil-safe/disabled and boot proceeds (the analytics surface is
// values-flagged anyway).
type GSCConfig struct {
	// ServiceAccountJSON is the decoded service-account key JSON
	// (GSC_SERVICE_ACCOUNT_JSON carries it base64-encoded). Empty disables
	// the provider.
	ServiceAccountJSON []byte
}

// GooglePSIConfig holds the PageSpeed Insights API key (audit engine's
// Performance category). Like GSC this is deliberately optional: an empty
// key leaves the PSI provider failing loudly per call, which the audit's
// collector degrades to a constraint — boot proceeds.
type GooglePSIConfig struct {
	APIKey string // GOOGLE_PSI_API_KEY
}

type AppConfig struct {
	Database     DatabaseConfig
	Server       ServerConfig
	Env          EnvConfig
	Clerk        ClerkConfig
	AWS          AWSConfig
	LLM          LLMConfig
	DataForSEO   dataForSeoConfigs
	SQS          SQSConfig
	AsyncHandler AsyncHandlerConfig
	Tavily       TavilyConfig
	YouTube      YouTubeConfig
	Paddle       PaddleConfig
	Sidecar      SidecarConfig
	Mailer       MailerConfig
	GSC          GSCConfig
	GooglePSI    GooglePSIConfig
	// Values holds the YAML-sourced runtime tunables + external API URLs. The
	// loaders below read it as their default layer (env vars still override).
	Values Values
}

// MailerConfig holds the SMTP transport for transactional email (the company
// invite-accept links, tenancy plan D3/D19). An empty Host disables sending —
// flows fall back to surfacing the link in the API response for the dashboard
// to relay.
type MailerConfig struct {
	Host     string // SMTP_HOST
	Port     string // SMTP_PORT (default "587")
	Username string // SMTP_USERNAME
	Password string // SMTP_PASSWORD
	From     string // SMTP_FROM
}

// LoadCompanyConfig loads the company-tenancy link templates and the SMTP
// mailer. The templates carry per-env YAML defaults (localhost on
// integration, the app domain in production); the env vars remain overrides
// for deployments whose frontend lives elsewhere.
func (c *AppConfig) LoadCompanyConfig() {
	if tmpl := strings.TrimSpace(os.Getenv("COMPANY_INVITE_URL_TEMPLATE")); tmpl != "" {
		c.Values.Company.InviteURLTemplate = tmpl
	}
	c.Mailer.Host = os.Getenv("SMTP_HOST")
	c.Mailer.Port = os.Getenv("SMTP_PORT")
	if c.Mailer.Port == "" {
		c.Mailer.Port = "587"
	}
	c.Mailer.Username = os.Getenv("SMTP_USERNAME")
	c.Mailer.Password = os.Getenv("SMTP_PASSWORD")
	c.Mailer.From = os.Getenv("SMTP_FROM")
}

func (c *AppConfig) LoadDatabaseConfig() {
	c.Database.URL = os.Getenv("MONGO_URI")
	c.Database.Name = os.Getenv("MONGO_DB_NAME")
	if c.Database.Name == "" {
		c.Database.Name = c.Values.Server.DefaultDBName
	}
}

func (c *AppConfig) LoadDataForSEOConfigs() {
	c.DataForSEO.Username = os.Getenv("DATAFORSEO_USERNAME")
	c.DataForSEO.Password = os.Getenv("DATAFORSEO_PASSWORD")

	if c.DataForSEO.Username == "" || c.DataForSEO.Password == "" {
		panic("DATAFORSEO_USERNAME and DATAFORSEO_PASSWORD must be set")
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
	c.AWS.S3Bucket = os.Getenv("AWS_S3_BUCKET")
}

func (c *EnvConfig) IsProd() bool {
	return c.Environment == envProduction
}

func (c *EnvConfig) IsDev() bool {
	return c.Environment == envDevelopment
}

// PaddleEnvironment derives the Paddle environment from the app environment.
// Only production talks to live Paddle; development (which doubles as staging)
// uses sandbox. This is code-controlled (not an env var) so a non-prod
// deployment can never be pointed at live billing by a mis-set variable.
func (c *EnvConfig) PaddleEnvironment() string {
	if c.IsProd() {
		return paddleEnvProduction
	}
	return paddleEnvSandbox
}

func (c *AppConfig) LoadLLMConfig() {
	c.LLM.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	c.LLM.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	c.LLM.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	c.LLM.DefaultProvider = os.Getenv("LLM_DEFAULT_PROVIDER")
	if c.LLM.DefaultProvider == "" {
		c.LLM.DefaultProvider = c.Values.LLM.DefaultProvider
	}
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

	clusterVisTimeout := atoiEnv("SQS_CLUSTERING_VISIBILITY_TIMEOUT")
	if clusterVisTimeout == 0 {
		clusterVisTimeout = c.Values.SQS.ClusteringVisibilityTimeout
	}
	c.SQS.ClusteringVisibilityTimeout = clusterVisTimeout

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

func (c *AppConfig) LoadTavilyConfig() {
	c.Tavily.APIKey = os.Getenv("TAVILY_API_KEY")
}

// LoadPaddleConfig loads the Paddle credentials. The Paddle environment is not
// read from the env — it's derived from ENVIRONMENT (see PaddleEnvironment) so
// a non-prod deployment can never be pointed at live billing by a stray var.
// Requires LoadEnvConfig to have run first.
func (c *AppConfig) LoadPaddleConfig() {
	c.Paddle.APIKey = os.Getenv("PADDLE_API_KEY")
	c.Paddle.WebhookSecret = os.Getenv("PADDLE_WEBHOOK_SECRET")
	c.Paddle.ProductID = os.Getenv("PADDLE_PRODUCT_ID")
	c.Paddle.TeamProductID = os.Getenv("PADDLE_TEAM_PRODUCT_ID")
	c.Paddle.Environment = c.Env.PaddleEnvironment()
}

func (c *AppConfig) LoadSidecarConfig() {
	c.Sidecar.SharedSecret = os.Getenv("SIDECAR_SHARED_SECRET")
}

// LoadGSCConfig decodes the Google Search Console service-account key.
// GSC_SERVICE_ACCOUNT_JSON carries the SA key JSON base64-encoded; unset
// leaves the provider disabled (boot must not crash), and malformed base64 is
// logged and treated as unset per the atoiEnv warn-and-degrade convention.
func (c *AppConfig) LoadGSCConfig() {
	raw := strings.TrimSpace(os.Getenv("GSC_SERVICE_ACCOUNT_JSON"))
	if raw == "" {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		log.Warn("malformed GSC_SERVICE_ACCOUNT_JSON (expected base64-encoded SA key JSON), GSC provider disabled", "error", err)
		return
	}
	c.GSC.ServiceAccountJSON = decoded
}

func (c *AppConfig) LoadYouTubeConfig() {
	c.YouTube.APIKey = os.Getenv("YOUTUBE_API_KEY")
	c.YouTube.WebshareUsername = os.Getenv("WEBSHARE_USERNAME")
	c.YouTube.WebsharePassword = os.Getenv("WEBSHARE_PASSWORD")
}

// LoadGooglePSIConfig reads the PageSpeed Insights key. Optional — see
// GooglePSIConfig.
func (c *AppConfig) LoadGooglePSIConfig() {
	c.GooglePSI.APIKey = os.Getenv("GOOGLE_PSI_API_KEY")
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
	c.LoadClerkConfig()
	c.LoadAWSConfig()
	c.LoadLLMConfig()
	c.LoadDataForSEOConfigs()
	c.LoadSQSConfig()
	c.LoadAsyncHandlerConfig()
	c.LoadTavilyConfig()
	c.LoadYouTubeConfig()
	c.LoadPaddleConfig()
	c.LoadSidecarConfig()
	c.LoadCompanyConfig()
	c.LoadGSCConfig()
	c.LoadGooglePSIConfig()
	return nil
}
