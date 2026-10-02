package providers

import (
	"os"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/pipeline"
	apiclient "github.com/atharva-ng/crunch/internal/providers/impl/apiClient"
	"github.com/atharva-ng/crunch/internal/providers/impl/clerkaccounts"
	"github.com/atharva-ng/crunch/internal/providers/impl/embed/voyage"
	"github.com/atharva-ng/crunch/internal/providers/impl/geocode"
	geminiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/gemini"
	openaiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/openai"
	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/anthropic"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	accountsvc "github.com/atharva-ng/crunch/internal/services/accountService/service"
	accountstore "github.com/atharva-ng/crunch/internal/services/accountService/store"
	aisearchsvc "github.com/atharva-ng/crunch/internal/services/aiSearchService/service"
	aisearchstore "github.com/atharva-ng/crunch/internal/services/aiSearchService/store"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	attributesvc "github.com/atharva-ng/crunch/internal/services/attributeService/service"
	attributestore "github.com/atharva-ng/crunch/internal/services/attributeService/store"
	catalogsvc "github.com/atharva-ng/crunch/internal/services/catalogService/service"
	catalogstore "github.com/atharva-ng/crunch/internal/services/catalogService/store"
	searchsvc "github.com/atharva-ng/crunch/internal/services/searchService/service"
	searchstore "github.com/atharva-ng/crunch/internal/services/searchService/store"
	usersvc "github.com/atharva-ng/crunch/internal/services/userservice/service"
	userstore "github.com/atharva-ng/crunch/internal/services/userservice/store"
	"github.com/atharva-ng/crunch/internal/tokentracker"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
)

// InjectDefaultProviders wires the infrastructure providers: S3, the token
// tracker, both SQS queues, the idempotency store, the LLM + image-generation
// clients, the shared HTTP client and the dispatcher.
func InjectDefaultProviders(appCtx *config.AppContext) error {
	if err := InjectDefaultS3Provider(appCtx); err != nil {
		return err
	}

	tracker := tokentracker.New(appCtx.Config.AsyncHandler.TokenLimit, appCtx.Config.AsyncHandler.TokenWindow)
	appCtx.TokenTracker = tracker
	if err := InjectDefaultSQSProvider(appCtx, tracker); err != nil {
		return err
	}

	if err := InjectDefaultIdempotencyStore(appCtx); err != nil {
		return err
	}

	llmValues := appCtx.Config.Values.LLM
	appCtx.InternalServices.LLM = &config.LLMProvider{DefaultMaxTokens: llmValues.DefaultMaxTokens}
	if appCtx.Config.LLM.AnthropicAPIKey != "" {
		appCtx.InternalServices.LLM.Anthropic = llmutil.NewTrackedLLM(
			anthropic.New(
				appCtx.Config.LLM.AnthropicAPIKey,
				llmValues.Anthropic.APIURL,
				llmValues.Anthropic.APIVersion,
				llmValues.Anthropic.FallbackModel,
				llmValues.Anthropic.FallbackMaxTokens,
				llmValues.Anthropic.RequestTimeoutSeconds,
				0,
			),
			tracker,
		)
		log.Info("Injected Anthropic provider (with token tracking)")
	}
	appCtx.InternalServices.LLM.Utils = llmutil.NewLlmUtils()

	appCtx.InternalServices.ImageGen = buildImageGenerator(appCtx)
	appCtx.APIClient = apiclient.GetClient(time.Duration(appCtx.Config.Values.APIs.HTTPClient.TimeoutSeconds) * time.Second)
	if key := appCtx.Config.Maps.GoogleAPIKey; key != "" {
		gv := appCtx.Config.Values.APIs.Geocode
		appCtx.Geocoder = geocode.NewGoogle(appCtx.APIClient, gv.APIURL, key, time.Duration(gv.TimeoutMillis)*time.Millisecond)
		log.Info("Injected Google geocoder")
	}

	// Dispatcher depends on Queue, so it's created after Queue injection.
	appCtx.InternalServices.Dispatcher = asynchandler.NewDispatcher(
		appCtx.QueueProvider,
		appCtx.SecondaryQueueProvider,
		llmProcessTypes,
		appCtx.Config.AsyncHandler.MaxRetries,
	)

	return nil
}

// buildImageGenerator picks the image provider named by
// values.apis.imageGen.defaultProvider. Nil when that provider's API key is
// unset — callers must handle a missing generator.
func buildImageGenerator(appCtx *config.AppContext) interfaces.ImageGenerator {
	imageGenValues := appCtx.Config.Values.APIs.ImageGen
	switch imageGenValues.DefaultProvider {
	case "openai":
		if appCtx.Config.LLM.OpenAIAPIKey == "" {
			return nil
		}
		openaiImage := imageGenValues.OpenAI
		log.Info("Injected OpenAI image generation provider")
		return openaiimage.New(
			appCtx.Config.LLM.OpenAIAPIKey,
			openaiImage.APIURL,
			openaiImage.Model,
			openaiImage.DefaultAspectRatio,
			time.Duration(openaiImage.RequestTimeoutSeconds)*time.Second,
		)
	default: // "gemini" or empty
		if appCtx.Config.LLM.GeminiAPIKey == "" {
			return nil
		}
		geminiImage := imageGenValues.Gemini
		log.Info("Injected Gemini image generation provider")
		return geminiimage.New(
			appCtx.Config.LLM.GeminiAPIKey,
			geminiImage.APIURL,
			geminiImage.DefaultAspectRatio,
			time.Duration(geminiImage.RequestTimeoutSeconds)*time.Second,
		)
	}
}

// llmProcessTypes are the process types routed to the secondary (LLM-gated)
// queue. None yet; AI search (05) adds its own.
var llmProcessTypes []pipeline.ProcessType

// InjectDefaultServices wires the services: users, the account-deletion
// cascade and the WarehouseHub services (attributes, then catalog, which
// evaluates with the attribute rules, then search, which also serves the
// catalog's 410 nearby list, then AI search on top of search).
func InjectDefaultServices(appCtx *config.AppContext) error {
	appCtx.InternalServices.UserService = usersvc.NewService(userstore.NewStore())

	appCtx.InternalServices.ClerkAccounts = clerkaccounts.GetProvider()
	appCtx.InternalServices.AccountService = accountsvc.NewService(
		accountstore.NewStore(),
		appCtx.InternalServices.ClerkAccounts,
		nil, // no feature stores user data yet (enquiries add a cleaner, D-019)
	)

	attributes := attributesvc.NewService(
		attributestore.NewStore(),
		appCtx.InternalServices.Dispatcher,
		changelog.New(),
		appCtx.Config.Values.WarehouseHub.Attributes,
	)
	appCtx.InternalServices.AttributeService = attributes

	appCtx.InternalServices.CatalogService = catalogsvc.NewService(
		catalogstore.NewStore(),
		attributes.Rules(),
		changelog.New(),
		appCtx.Geocoder,
		appCtx.InternalServices.Dispatcher,
		appCtx.S3Provider,
		appCtx.Config.AWS,
		appCtx.Config.Values.Storage,
		appCtx.Config.Values.WarehouseHub,
	)
	search := searchsvc.NewService(
		searchstore.NewStore(),
		attributes.Rules(),
		appCtx.Geocoder,
		appCtx.Config.Values.WarehouseHub,
		appCtx.Config.AWS,
	)
	appCtx.InternalServices.SearchService = search
	appCtx.InternalServices.CatalogService.SetSearchEngine(search)

	// AI search gets its own Anthropic client: one attempt, no backoff, no
	// token-tracker gate; the service's deadline bounds the call (spec 05).
	aiValues := appCtx.Config.Values.WarehouseHub.AISearch
	var searchLLM interfaces.LlmService
	if key := appCtx.Config.LLM.AnthropicAPIKey; key != "" {
		llmValues := appCtx.Config.Values.LLM.Anthropic
		searchLLM = anthropic.New(key, llmValues.APIURL, llmValues.APIVersion, aiValues.Model, aiValues.MaxTokens,
			llmValues.RequestTimeoutSeconds, 1)
	}
	var embedder interfaces.Embedder
	if key := appCtx.Config.LLM.VoyageAPIKey; key != "" {
		embedder = voyage.New(appCtx.APIClient, aiValues.VoyageURL, key, aiValues.EmbedModel, aiValues.EmbedDims,
			time.Duration(aiValues.EmbedTimeoutMillis)*time.Millisecond)
		log.Info("Injected Voyage embedder", "model", aiValues.EmbedModel)
	}
	appCtx.InternalServices.AISearchService = aisearchsvc.NewService(
		aisearchstore.NewStore(),
		attributes.Rules(),
		search,
		searchLLM,
		embedder,
		appCtx.InternalServices.Dispatcher,
		aiValues,
	)
	log.Info("Injected WarehouseHub services")

	return nil
}

func buildServiceLocator(appCtx *config.AppContext) *asynchandler.ServiceLocator {
	return &asynchandler.ServiceLocator{
		AttributeService: appCtx.InternalServices.AttributeService,
		CatalogService:   appCtx.InternalServices.CatalogService,
		AISearchService:  appCtx.InternalServices.AISearchService,
	}
}

// BuildAsyncHandler creates the primary async handler.
// Must be called after InjectDefaultServices.
func BuildAsyncHandler(appCtx *config.AppContext) *asynchandler.AsyncHandler {
	return asynchandler.NewAsyncHandler(
		appCtx.QueueProvider,
		appCtx.IdempotencyStore,
		asynchandler.BuildProcessRegistry(buildServiceLocator(appCtx)),
		appCtx.Config.AsyncHandler.WorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		nil,
	)
}

// BuildSecondaryAsyncHandler creates the async handler for the secondary
// (LLM) queue with its own worker pool.
func BuildSecondaryAsyncHandler(appCtx *config.AppContext) *asynchandler.AsyncHandler {
	return asynchandler.NewAsyncHandler(
		appCtx.SecondaryQueueProvider,
		appCtx.IdempotencyStore,
		asynchandler.BuildProcessRegistry(buildServiceLocator(appCtx)),
		appCtx.Config.AsyncHandler.LLMWorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		nil,
	)
}

// BuildCronScheduler assembles the cron layer: job registry (code
// definitions + values times/flags) → scheduler (ticker + Mongo occurrence
// claims). Must be called after InjectDefaultServices, and started in main
// alongside the other background loops.
func BuildCronScheduler(appCtx *config.AppContext) (*cron.Scheduler, error) {
	values := appCtx.Config.Values.Cron
	jobs, err := cron.BuildJobRegistry(&cron.ServiceLocator{DefaultZone: values.DefaultZone}, values)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return cron.NewScheduler(cron.SchedulerConfig{
		Jobs:        jobs,
		Store:       cron.NewMongoStore(),
		Dispatcher:  appCtx.InternalServices.Dispatcher,
		Tick:        time.Duration(values.TickSeconds) * time.Second,
		StaleAfter:  time.Duration(values.ClaimStaleSeconds) * time.Second,
		DefaultZone: values.DefaultZone,
		Host:        host,
	})
}
