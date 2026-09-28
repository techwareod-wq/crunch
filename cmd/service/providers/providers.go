package providers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/atharva-ng/crunch/internal/analytics"
	dfsdomainrating "github.com/atharva-ng/crunch/internal/analytics/sources/dfsdomainrating"
	gscsource "github.com/atharva-ng/crunch/internal/analytics/sources/gsc"
	"github.com/atharva-ng/crunch/internal/audit"
	auditsvc "github.com/atharva-ng/crunch/internal/audit/service"
	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/pipeline"
	apiclient "github.com/atharva-ng/crunch/internal/providers/impl/apiClient"
	"github.com/atharva-ng/crunch/internal/providers/impl/clerkaccounts"
	dataforseo "github.com/atharva-ng/crunch/internal/providers/impl/dataForSEO"
	gscimpl "github.com/atharva-ng/crunch/internal/providers/impl/gsc"
	geminiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/gemini"
	openaiimage "github.com/atharva-ng/crunch/internal/providers/impl/imageGen/openai"
	paddleimpl "github.com/atharva-ng/crunch/internal/providers/impl/paddle"
	psiimpl "github.com/atharva-ng/crunch/internal/providers/impl/psi"
	"github.com/atharva-ng/crunch/internal/providers/impl/tavily"
	"github.com/atharva-ng/crunch/internal/providers/impl/youtube"

	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/anthropic"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/gemini"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm/openai"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	accountsvc "github.com/atharva-ng/crunch/internal/services/accountService/service"
	accountstore "github.com/atharva-ng/crunch/internal/services/accountService/store"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms/sidecar"
	cbsvc "github.com/atharva-ng/crunch/internal/services/contentBridge/service"
	cbstore "github.com/atharva-ng/crunch/internal/services/contentBridge/store"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgesvc "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/service"
	cgestore "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/store"
	onboardingsvc "github.com/atharva-ng/crunch/internal/services/onboardingService/service"
	onboardingstore "github.com/atharva-ng/crunch/internal/services/onboardingService/store"
	paymentsvc "github.com/atharva-ng/crunch/internal/services/paymentService/service"
	paymentstore "github.com/atharva-ng/crunch/internal/services/paymentService/store"
	schedarticlesvc "github.com/atharva-ng/crunch/internal/services/scheduledArticleService/service"
	schedarticlestore "github.com/atharva-ng/crunch/internal/services/scheduledArticleService/store"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	sesvc "github.com/atharva-ng/crunch/internal/services/schedulingEngine/service"
	sestore "github.com/atharva-ng/crunch/internal/services/schedulingEngine/store"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	siesvc "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/service"
	siestore "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/store"
	stylerep "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	stylerepsvc "github.com/atharva-ng/crunch/internal/services/styleReplicationService/service"
	usersvc "github.com/atharva-ng/crunch/internal/services/userservice/service"
	userstore "github.com/atharva-ng/crunch/internal/services/userservice/store"
	"github.com/atharva-ng/crunch/internal/tokentracker"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// LLMProcessTypes defines which process types route to the secondary (rate-limited) queue.
var LLMProcessTypes = []pipeline.ProcessType{
	sie.ProcessSIEFunnelClassification,
	sie.ProcessSIEAddManualKeyword,
	cge.ProcessCGEOutlineGeneration,
	cge.ProcessCGEArticleGeneration,
	cge.ProcessCGESchemaGeneration,
	cge.ProcessCGEMetaAssetsGeneration,
	cge.ProcessCGEYouTubeSummary,
	se.ProcessSchedulingGenerateArticleType,
	se.ProcessSchedulingGenerateTitle,
	stylerep.ProcessStyleSynthesis,
	audit.ProcessAuditJudgeContent,
	audit.ProcessAuditSynthesize,
}

// auditCrawlVisibilityHeadroomSeconds tops the AUDIT_CRAWL visibility
// override above the in-handler poll window so the message can't be
// redelivered mid-poll.
const auditCrawlVisibilityHeadroomSeconds = 120

// visibilityOverrides maps process types whose handlers run longer than the
// SQS default to a per-message visibility timeout (seconds). The consumer
// extends the message's visibility on receipt; everything else keeps the
// queue default.
func visibilityOverrides(cfg config.SQSConfig, auditVals config.AuditValues) map[pipeline.ProcessType]int32 {
	overrides := map[pipeline.ProcessType]int32{}
	if cfg.ClusteringVisibilityTimeout > 0 {
		overrides[sie.ProcessSIEClustering] = int32(cfg.ClusteringVisibilityTimeout)
	}
	if auditVals.Crawl.PollTimeoutSeconds > 0 {
		overrides[audit.ProcessAuditCrawl] = int32(auditVals.Crawl.PollTimeoutSeconds + auditCrawlVisibilityHeadroomSeconds)
	}
	return overrides
}

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
	if appCtx.Config.LLM.OpenAIAPIKey != "" {
		appCtx.InternalServices.LLM.OpenAI = openai.New(appCtx.Config.LLM.OpenAIAPIKey, llmValues.OpenAI.APIURL, llmValues.OpenAI.FallbackModel)
		log.Info("Injected OpenAI provider")
	}
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
		log.Info("1Injected Anthropic provider (with token tracking)")
	}
	if appCtx.Config.LLM.GeminiAPIKey != "" {
		appCtx.InternalServices.LLM.Gemini = gemini.New(appCtx.Config.LLM.GeminiAPIKey, llmValues.Gemini.APIURL, llmValues.Gemini.FallbackModel)
		log.Info("Injected Gemini provider")
	}
	appCtx.InternalServices.LLM.Utils = llmutil.NewLlmUtils()

	// Dispatcher depends on Queue, so it's created after Queue injection.
	appCtx.InternalServices.Dispatcher = asynchandler.NewDispatcher(
		appCtx.QueueProvider,
		appCtx.SecondaryQueueProvider,
		LLMProcessTypes,
		appCtx.Config.AsyncHandler.MaxRetries,
	)

	return nil
}

func InjectDefaultServices(appCtx *config.AppContext) error {
	uStore := userstore.NewStore()
	appCtx.InternalServices.UserService = usersvc.NewService(uStore)

	sieStore := siestore.NewStore()
	apiClient := apiclient.GetClient(time.Duration(appCtx.Config.Values.APIs.HTTPClient.TimeoutSeconds) * time.Second)
	dataForSEOProvider := dataforseo.GetProvider(apiClient, appCtx.Config.DataForSEO.Username, appCtx.Config.DataForSEO.Password)

	// Shared boot-time country catalog (one GetLocations fetch, consumed by
	// onboarding validation + audit market resolution). Locations are
	// critical for every location-scoped SERP call — a load failure is a
	// startup error. Runs at boot, before any request context exists.
	locationCatalog, err := locations.Load(context.Background(), dataForSEOProvider)
	if err != nil {
		return fmt.Errorf("providers: load location catalog: %w", err)
	}

	// Analytics engine sources. The registry can't import the source packages
	// (they import it for the Source contract), so sources are constructed
	// WITH their providers here and registered by name — this is the one
	// wiring site a new source adds a line to. The GSC provider is nil-safe:
	// no SA key ⇒ disabled provider, boot proceeds, fetches fail loudly.
	gscProvider := gscimpl.GetProvider(appCtx.Config.GSC.ServiceAccountJSON)
	appCtx.GSCProvider = gscProvider
	analytics.Register(gscsource.New(gscProvider, appCtx.Config.Values.GSC))
	analytics.Register(dfsdomainrating.New(dataForSEOProvider))

	appCtx.InternalServices.SiteIntelligenceService = siesvc.NewService(sieStore, appCtx.InternalServices.LLM, dataForSEOProvider, appCtx.InternalServices.Dispatcher, appCtx.Config.Values.SiteIntelligence)

	// Plan catalog cache. Boot-loads all plan docs (hard-fail on DB error,
	// zero docs tolerated — the binary deploys before the seed run); the
	// refresh ticker starts in main once the shutdown context exists. Built
	// before the payment service, which resolves webhook prices against it.
	plansCache, err := entitlements.NewPlansCache(context.Background())
	if err != nil {
		return err
	}
	appCtx.PlansCache = plansCache
	log.Info("Injected plans cache")

	// Role catalog cache (RBAC). Boot-loads all role docs (hard-fail on DB
	// error, empty catalog tolerated + logged — the binary deploys before the
	// seed run); the refresh ticker starts in main once the shutdown context
	// exists. Backs the admin authorization gate.
	rolesCache, err := authz.NewRolesCache(context.Background())
	if err != nil {
		return err
	}
	appCtx.RolesCache = rolesCache
	log.Info("Injected roles cache")

	// Payments (Paddle). Built before onboarding because GetOnboardingSteps now
	// gates its SIE trigger on a valid subscription. Never log the API key.
	paddleValues := appCtx.Config.Values.Paddle
	paddleProvider, err := paddleimpl.GetProvider(
		appCtx.Config.Paddle.APIKey,
		appCtx.Config.Paddle.WebhookSecret,
		appCtx.Config.Paddle.Environment,
		time.Duration(paddleValues.APICallTimeoutSeconds)*time.Second,
		time.Duration(paddleValues.PriceCacheTTLSeconds)*time.Second,
		time.Duration(paddleValues.WebhookTimestampToleranceSeconds)*time.Second,
	)
	if err != nil {
		return err
	}
	appCtx.PaddleProvider = paddleProvider
	appCtx.InternalServices.PaymentService = paymentsvc.NewService(
		paymentstore.NewStore(),
		paddleProvider,
		plansCache,
		appCtx.InternalServices.Dispatcher,
		appCtx.Config.Paddle.ProductID,
		appCtx.Config.Paddle.TeamProductID,
		appCtx.Config.Values.SiteIntelligence.Trial.MaxArticles,
	)
	log.Info("Injected Paddle payment provider", "environment", appCtx.Config.Paddle.Environment)

	appCtx.InternalServices.AccountService = accountsvc.NewService(
		accountstore.NewStore(),
		appCtx.InternalServices.PaymentService,
		appCtx.S3Provider,
		appCtx.Config.AWS.S3Bucket,
		clerkaccounts.GetProvider(),
	)

	onbStore := onboardingstore.NewStore()
	appCtx.InternalServices.OnboardingService = onboardingsvc.NewSeoBlogGeneratorOnboardingService(
		onbStore,
		appCtx.InternalServices.LLM,
		dataForSEOProvider,
		appCtx.InternalServices.Dispatcher,
		appCtx.InternalServices.PaymentService,
		appCtx.InternalServices.SiteIntelligenceService,
		appCtx.Config.Values.Onboarding,
		appCtx.Config.Values.APIs.DataForSEO,
		locationCatalog,
	)

	tavilyProvider := tavily.GetProvider(apiClient, appCtx.Config.Tavily.APIKey, appCtx.Config.Values.APIs.Tavily.SearchURL)

	youtubeProvider := youtube.GetProvider(apiClient, appCtx.Config.YouTube.APIKey, appCtx.Config.YouTube.WebshareUsername, appCtx.Config.YouTube.WebsharePassword, appCtx.Config.Values.APIs.YouTube.SearchURL)

	var imageGenProvider interfaces.ImageGenerator
	imageGenValues := appCtx.Config.Values.APIs.ImageGen
	switch imageGenValues.DefaultProvider {
	case "openai":
		if appCtx.Config.LLM.OpenAIAPIKey != "" {
			openaiImage := imageGenValues.OpenAI
			imageGenProvider = openaiimage.New(
				appCtx.Config.LLM.OpenAIAPIKey,
				openaiImage.APIURL,
				openaiImage.Model,
				openaiImage.DefaultAspectRatio,
				time.Duration(openaiImage.RequestTimeoutSeconds)*time.Second,
			)
			log.Info("Injected OpenAI image generation provider")
		}
	default: // "gemini" or empty
		if appCtx.Config.LLM.GeminiAPIKey != "" {
			geminiImage := imageGenValues.Gemini
			imageGenProvider = geminiimage.New(
				appCtx.Config.LLM.GeminiAPIKey,
				geminiImage.APIURL,
				geminiImage.DefaultAspectRatio,
				time.Duration(geminiImage.RequestTimeoutSeconds)*time.Second,
			)
			log.Info("Injected Gemini image generation provider")
		}
	}

	cgeStoreInst := cgestore.NewStore()
	appCtx.InternalServices.ContentGenerationService = cgesvc.NewService(
		cgeStoreInst,
		appCtx.InternalServices.LLM,
		dataForSEOProvider,
		tavilyProvider,
		youtubeProvider,
		imageGenProvider,
		appCtx.InternalServices.Dispatcher,
		appCtx.S3Provider,
		appCtx.Config.AWS.S3Bucket,
		appCtx.Config.Values.ContentGeneration,
	)

	appCtx.InternalServices.StyleReplicationService = stylerepsvc.NewService(
		appCtx.InternalServices.LLM,
		dataForSEOProvider,
		appCtx.InternalServices.Dispatcher,
		appCtx.S3Provider,
		appCtx.Config.AWS.S3Bucket,
		appCtx.Config.Values.StyleReplication,
		appCtx.Config.Values.APIs.DataForSEO,
	)

	// Audit engine. NewService builds the spec + collector/check registries
	// and BOOT-FAILS on any inconsistency (missing artifact producer,
	// allocation not summing to 100, unknown findings-only id, ...). The PSI
	// provider is key-optional: empty key ⇒ per-call failure ⇒ the PSI
	// collector degrades to a constraint.
	psiProvider := psiimpl.GetProvider(apiClient, appCtx.Config.GooglePSI.APIKey, appCtx.Config.Values.APIs.PSI.RunPagespeedURL)
	auditService, err := auditsvc.NewService(
		appCtx.InternalServices.LLM,
		dataForSEOProvider,
		psiProvider,
		appCtx.InternalServices.Dispatcher,
		appCtx.Config.Values.Audit,
		appCtx.Config.Values.APIs.DataForSEO,
		locationCatalog,
	)
	if err != nil {
		return err
	}
	appCtx.InternalServices.AuditService = auditService
	log.Info("Injected audit service")

	seStoreInst := sestore.NewStore()
	appCtx.InternalServices.SchedulingService = sesvc.NewService(
		seStoreInst,
		appCtx.InternalServices.LLM,
		appCtx.InternalServices.Dispatcher,
		appCtx.Config.Values.Scheduling,
		appCtx.Config.Values.SiteIntelligence.Trial,
	)

	appCtx.InternalServices.ScheduledArticleService = schedarticlesvc.NewService(
		schedarticlestore.NewStore(),
		appCtx.S3Provider,
		appCtx.Config.AWS.S3Bucket,
	)

	// Content bridge: per-platform publisher adapters behind a registry. Adding
	// a platform = build its publisher and pass it to NewRegistry; no service
	// core changes. Framer is served by the Node sidecar (the Framer Server
	// API is an npm SDK speaking a stateful WebSocket — no REST contract to
	// call from Go); a second sidecar-hosted platform is one more New(...)
	// with a different provider name.
	// A Framer publish includes a full site build — far beyond the shared
	// ApiClient's 60s cap, hence the dedicated client. A non-positive timeout is
	// a config error rather than something to paper over: http.Client treats 0 as
	// "no timeout", so a missing key would silently make publishes unbounded.
	sidecarValues := appCtx.Config.Values.APIs.Sidecar
	if sidecarValues.TimeoutSeconds <= 0 {
		return fmt.Errorf("apis.sidecar.timeoutSeconds must be > 0, got %d", sidecarValues.TimeoutSeconds)
	}
	sidecarClient := &http.Client{Timeout: time.Duration(sidecarValues.TimeoutSeconds) * time.Second}
	framerPublisher := sidecar.New(
		sidecarClient,
		sidecarValues.BaseURL,
		appCtx.Config.Sidecar.SharedSecret,
		"framer",
	)
	payloadPublisher := sidecar.New(
		sidecarClient,
		sidecarValues.BaseURL,
		appCtx.Config.Sidecar.SharedSecret,
		"payload",
	)
	contentBridgeRegistry := platforms.NewRegistry(framerPublisher, payloadPublisher)
	appCtx.InternalServices.ContentBridgeService = cbsvc.NewService(
		cbstore.NewStore(),
		contentBridgeRegistry,
		appCtx.S3Provider,
		appCtx.Config.AWS.S3Bucket,
	)

	return nil
}

func buildServiceLocator(appCtx *config.AppContext) *asynchandler.ServiceLocator {
	return &asynchandler.ServiceLocator{
		UserService:              appCtx.InternalServices.UserService,
		OnboardingService:        appCtx.InternalServices.OnboardingService,
		SiteIntelligenceService:  appCtx.InternalServices.SiteIntelligenceService,
		ContentGenerationService: appCtx.InternalServices.ContentGenerationService,
		SchedulingService:        appCtx.InternalServices.SchedulingService,
		ContentBridgeService:     appCtx.InternalServices.ContentBridgeService,
		StyleReplicationService:  appCtx.InternalServices.StyleReplicationService,
		AuditService:             appCtx.InternalServices.AuditService,
	}
}

// BuildAsyncHandler creates the primary async handler.
// Must be called after InjectDefaultServices.
func BuildAsyncHandler(appCtx *config.AppContext) *asynchandler.AsyncHandler {
	registry := asynchandler.BuildProcessRegistry(buildServiceLocator(appCtx))

	return asynchandler.NewAsyncHandler(
		appCtx.QueueProvider,
		appCtx.IdempotencyStore,
		registry,
		appCtx.Config.AsyncHandler.WorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		visibilityOverrides(appCtx.Config.SQS, appCtx.Config.Values.Audit),
	)
}

// BuildSecondaryAsyncHandler creates the async handler for the secondary
// (LLM) queue with its own worker pool.
func BuildSecondaryAsyncHandler(appCtx *config.AppContext) *asynchandler.AsyncHandler {
	registry := asynchandler.BuildProcessRegistry(buildServiceLocator(appCtx))

	return asynchandler.NewAsyncHandler(
		appCtx.SecondaryQueueProvider,
		appCtx.IdempotencyStore,
		registry,
		appCtx.Config.AsyncHandler.LLMWorkerCount,
		appCtx.Config.AsyncHandler.MaxRetries,
		appCtx.Config.AsyncHandler.ShutdownTimeout,
		visibilityOverrides(appCtx.Config.SQS, appCtx.Config.Values.Audit),
	)
}

// BuildCronScheduler assembles the cron layer: job registry (code definitions
// + values times/flags) → scheduler (5m ticker + Mongo occurrence claims).
// Must be called after InjectDefaultServices (resolvers read the plans cache),
// and started in main alongside the other background loops.
func BuildCronScheduler(appCtx *config.AppContext) (*cron.Scheduler, error) {
	values := appCtx.Config.Values.Cron
	jobs, err := cron.BuildJobRegistry(&cron.ServiceLocator{
		PlansCache:  appCtx.PlansCache,
		DefaultZone: values.DefaultZone,
	}, values)
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
