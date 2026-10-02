package providers

import (
	"os"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/modules"
	apiclient "github.com/atharva-ng/crunch/internal/providers/impl/apiClient"
	"github.com/atharva-ng/crunch/internal/providers/impl/clerkaccounts"
	"github.com/atharva-ng/crunch/internal/providers/impl/geocode"
	geminiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/gemini"
	openaiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/openai"
	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/anthropic"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	accountsvc "github.com/atharva-ng/crunch/internal/services/accountService/service"
	accountstore "github.com/atharva-ng/crunch/internal/services/accountService/store"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	usersvc "github.com/atharva-ng/crunch/internal/services/userservice/service"
	userstore "github.com/atharva-ng/crunch/internal/services/userservice/store"
	"github.com/atharva-ng/crunch/internal/tokentracker"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// InjectDefaultProviders wires the infrastructure providers: S3, the token
// tracker, both SQS queues, the idempotency store, the LLM + image-generation
// clients, the shared HTTP client and the dispatcher. mods supply
// the process types routed to the secondary (LLM-gated) queue.
func InjectDefaultProviders(appCtx *config.AppContext, mods []modules.Module) error {
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
		modules.LLMProcessTypes(mods),
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

// InjectDefaultServices wires the platform services: users, the roles cache
// and the account-deletion cascade (with every module's DataCleaners).
func InjectDefaultServices(appCtx *config.AppContext, mods []modules.Module) error {
	appCtx.InternalServices.UserService = usersvc.NewService(userstore.NewStore())

	appCtx.InternalServices.ClerkAccounts = clerkaccounts.GetProvider()
	appCtx.InternalServices.AccountService = accountsvc.NewService(
		accountstore.NewStore(),
		appCtx.InternalServices.ClerkAccounts,
		modules.DataCleaners(mods),
	)

	return nil
}

// BuildAsyncHandler creates the primary async handler.
// Must be called after InjectDefaultServices.
func BuildAsyncHandler(appCtx *config.AppContext, mods []modules.Module) *asynchandler.AsyncHandler {
	return asynchandler.NewAsyncHandler(
		appCtx.QueueProvider,
		appCtx.IdempotencyStore,
		modules.BuildRegistry(mods),
		appCtx.Config.AsyncHandler.WorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		modules.VisibilityOverrides(mods),
	)
}

// BuildSecondaryAsyncHandler creates the async handler for the secondary
// (LLM) queue with its own worker pool.
func BuildSecondaryAsyncHandler(appCtx *config.AppContext, mods []modules.Module) *asynchandler.AsyncHandler {
	return asynchandler.NewAsyncHandler(
		appCtx.SecondaryQueueProvider,
		appCtx.IdempotencyStore,
		modules.BuildRegistry(mods),
		appCtx.Config.AsyncHandler.LLMWorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		modules.VisibilityOverrides(mods),
	)
}

// BuildCronScheduler assembles the cron layer: job registry (module
// definitions + values times/flags) → scheduler (ticker + Mongo occurrence
// claims). Must be called after InjectDefaultServices, and started in main
// alongside the other background loops.
func BuildCronScheduler(appCtx *config.AppContext, mods []modules.Module) (*cron.Scheduler, error) {
	values := appCtx.Config.Values.Cron
	jobs, err := cron.BuildJobRegistry(modules.CronJobs(mods), values)
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
