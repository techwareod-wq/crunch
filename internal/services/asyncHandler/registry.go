package asyncHandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/onboardingService"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	stylerep "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/services/userservice"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func BuildProcessRegistry(locator *ServiceLocator) map[pipeline.ProcessType]ProcessHandler {
	sieHandler := buildSIEHandler(locator)

	registry := map[pipeline.ProcessType]ProcessHandler{
		sie.ProcessSiteIntelligence:              buildSiteIntelligenceOrchestratorHandler(locator),
		sie.ProcessSIEGetUserKeywords:            sieHandler(locator.SiteIntelligenceService.GetUserKeywords),
		sie.ProcessSIEGetCompetitorKeywords:      sieHandler(locator.SiteIntelligenceService.GetCompetitorKeywords),
		sie.ProcessSIEGetPreExpandedUserKeywords: sieHandler(locator.SiteIntelligenceService.GetPreExpandedUserKeywords),
		sie.ProcessSIEGetExpandedUserKeywords:    sieHandler(locator.SiteIntelligenceService.GetExpandedUserKeywords),
		sie.ProcessSIEPostProcessing:             sieHandler(locator.SiteIntelligenceService.PostProcessing),
		sie.ProcessSIEFunnelClassification:       buildFunnelClassificationHandler(locator),
		sie.ProcessSIEOpportunityScore:           sieHandler(locator.SiteIntelligenceService.CalculateOpportunityScore),
		sie.ProcessSIEClustering:                 buildClusteringHandler(locator),
		sie.ProcessSIEAddManualKeyword:           buildManualKeywordEnrichmentHandler(locator),
		sie.ProcessSIEUpgradeExpand:              buildUpgradeExpandHandler(locator),
	}

	registerOnboardingHandlers(registry, locator)
	registerCGEHandlers(registry, locator)
	registerSchedulingHandlers(registry, locator)
	registerContentBridgeHandlers(registry, locator)
	registerEntitlementsHandlers(registry)
	registerAnalyticsHandlers(registry)
	registerStyleReplicationHandlers(registry, locator)
	registerAuditHandlers(registry, locator)

	return registry
}

// registerAuditHandlers wires the audit pipeline's five stages. The
// single-flight stages (crawl/score/judge/synthesize) stamp the run's typed
// terminal error on the failure that ends them for good — a permanent error
// or the last allowed attempt — so the UI poll terminates instead of
// spinning (the styleReplication wrapper pattern). The collect fan-out is
// different: a collect message that dies for good routes into
// HandleCollectFailure, which records a constraint and completes the fan-in
// — a non-critical data source must never wedge the run (§8.3).
func registerAuditHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	svc := locator.AuditService

	runHandler := func(fn func(ctx context.Context, userID string, p audit.AuditRunPayload) error) ProcessHandler {
		return func(ctx context.Context, msg MessageEnvelope) error {
			var p audit.AuditRunPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return fmt.Errorf("unmarshal audit payload: %w", err)
			}
			if err := fn(ctx, msg.UserID, p); err != nil {
				finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
				if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
					if markErr := models.SetAuditRunError(ctx, p.RunID, string(msg.ProcessType), err.Error(),
						auditFailureReason(err), errors.Is(err, pipeline.ErrPermanent)); markErr != nil {
						log.Error("failed to mark audit run error", "error", markErr, "runId", p.RunID, "processType", msg.ProcessType)
					}
				}
				return err
			}
			return nil
		}
	}

	registry[audit.ProcessAuditCrawl] = runHandler(svc.HandleCrawl)
	registry[audit.ProcessAuditScore] = runHandler(svc.HandleScore)
	registry[audit.ProcessAuditJudgeContent] = runHandler(svc.HandleJudgeContent)
	registry[audit.ProcessAuditSynthesize] = runHandler(svc.HandleSynthesize)

	// Re-check: single scoped stage on the primary queue. Its terminal error
	// stamps the RECHECK doc (the run is untouched — score honesty).
	registry[audit.ProcessAuditRecheck] = func(ctx context.Context, msg MessageEnvelope) error {
		var p audit.AuditRecheckPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal audit recheck payload: %w", err)
		}
		if err := svc.HandleRecheck(ctx, msg.UserID, p); err != nil {
			finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
			if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
				if markErr := models.SetAuditRecheckError(ctx, p.RecheckID, err.Error()); markErr != nil {
					log.Error("failed to mark audit recheck error", "error", markErr, "recheckId", p.RecheckID)
				}
			}
			return err
		}
		return nil
	}

	registry[audit.ProcessAuditCollect] = func(ctx context.Context, msg MessageEnvelope) error {
		var p audit.AuditCollectPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal audit collect payload: %w", err)
		}
		if err := svc.HandleCollect(ctx, msg.UserID, p); err != nil {
			finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
			if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
				if failErr := svc.HandleCollectFailure(ctx, p, err.Error()); failErr != nil {
					log.Error("failed to degrade audit collector failure", "error", failErr, "runId", p.RunID, "collectorId", p.CollectorID)
				}
			}
			return err
		}
		return nil
	}
}

// auditFailureReason extracts the typed crawl-failure code a handler wove
// into its error text ("[target_unreachable]" etc.) so the terminal error
// carries the machine-readable reason for the FE.
func auditFailureReason(err error) string {
	msg := err.Error()
	for _, reason := range []string{
		audit.AuditReasonTargetUnreachable,
		audit.AuditReasonRobotsBlocked,
		audit.AuditReasonRateLimitedByTarget,
		audit.AuditReasonTimeout,
	} {
		if strings.Contains(msg, "["+reason+"]") {
			return reason
		}
	}
	return ""
}

// registerStyleReplicationHandlers wires the style replication run steps. On
// the failure that ends a step for good — a permanent error, or the last
// allowed attempt — the run is flipped to its terminal error state so the
// wizard's poll stops instead of spinning forever (the buildOnboardingHandler
// pattern). Scrape is exempt from run-level error stamping on non-final
// failures: per-URL failures are already recorded on the run, and the run
// errors itself only when every URL failed.
func registerStyleReplicationHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	svc := locator.StyleReplicationService

	runHandler := func(fn func(ctx context.Context, userID string, p stylerep.StyleRunPayload) error) ProcessHandler {
		return func(ctx context.Context, msg MessageEnvelope) error {
			var p stylerep.StyleRunPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return fmt.Errorf("unmarshal style replication payload: %w", err)
			}
			if err := fn(ctx, msg.UserID, p); err != nil {
				finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
				if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
					if markErr := models.SetStyleRunError(ctx, p.RunID, err.Error()); markErr != nil {
						log.Error("failed to mark style replication run error", "error", markErr, "runId", p.RunID, "processType", msg.ProcessType)
					}
				}
				return err
			}
			return nil
		}
	}

	registry[stylerep.ProcessStyleDiscovery] = runHandler(svc.HandleDiscovery)
	registry[stylerep.ProcessStyleSynthesis] = runHandler(svc.HandleSynthesis)

	registry[stylerep.ProcessStyleScrape] = func(ctx context.Context, msg MessageEnvelope) error {
		var p stylerep.StyleScrapePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal style replication scrape payload: %w", err)
		}
		if err := svc.HandleScrape(ctx, msg.UserID, p); err != nil {
			// A scrape message that dies for good would strand the fan-in
			// counter above zero and the run in `scraping` forever — error the
			// run so the wizard can offer a retry.
			finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
			if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
				if markErr := models.SetStyleRunError(ctx, p.RunID, fmt.Sprintf("scrape of %s failed: %v", p.URL, err)); markErr != nil {
					log.Error("failed to mark style replication run error", "error", markErr, "runId", p.RunID)
				}
			}
			return err
		}
		return nil
	}
}

// registerAnalyticsHandlers wires the analytics engine's two shared processes.
// Every source's cron beat (and the verify endpoint's immediate first ingest)
// funnels through the ONE ingest handler — the source name travels in the
// payload, so new sources need no registration here.
func registerAnalyticsHandlers(registry map[pipeline.ProcessType]ProcessHandler) {
	registry[analytics.ProcessAnalyticsIngest] = func(ctx context.Context, msg MessageEnvelope) error {
		var p analytics.IngestPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal analytics ingest payload: %w", err)
		}
		return analytics.ProcessIngest(ctx, p)
	}
	registry[analytics.ProcessAnalyticsReplay] = func(ctx context.Context, msg MessageEnvelope) error {
		var p analytics.ReplayPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal analytics replay payload: %w", err)
		}
		// A failed unit is recorded on the tracking doc only on its LAST
		// delivery, so SQS retries don't double-count failures.
		finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
		return analytics.ProcessReplayChunk(ctx, p, finalAttempt)
	}
}

// registerEntitlementsHandlers wires the trial lifecycle consumers. The
// webhook's two-marker dispatch delivers AT-LEAST-ONCE, so both handlers must
// stay idempotent — key any future side effect (email, credits) on
// p.PaddleSubscriptionID. v1: structured log only.
func registerEntitlementsHandlers(registry map[pipeline.ProcessType]ProcessHandler) {
	registry[entitlements.ProcessTrialConverted] = func(ctx context.Context, msg MessageEnvelope) error {
		var p entitlements.TrialConvertedPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal trial converted payload: %w", err)
		}
		log.Info("trial converted",
			"user_id", msg.UserID, "app", p.AppID,
			"paddle_subscription_id", p.PaddleSubscriptionID,
			"price_id", p.PriceID, "converted_at", p.ConvertedAt)
		return nil
	}
	registry[entitlements.ProcessTrialExpired] = func(ctx context.Context, msg MessageEnvelope) error {
		var p entitlements.TrialExpiredPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal trial expired payload: %w", err)
		}
		log.Info("trial expired without conversion",
			"user_id", msg.UserID, "app", p.AppID,
			"paddle_subscription_id", p.PaddleSubscriptionID,
			"price_id", p.PriceID, "expired_at", p.ExpiredAt)
		return nil
	}
}

func registerOnboardingHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	svc := locator.OnboardingService

	registry[onboardingService.ProcessOnboardingWebEntity] = buildOnboardingHandler(svc.ProcessOnboardedUser)
	registry[onboardingService.ProcessOnboardingCompetitorInfo] = buildOnboardingHandler(svc.GetCompetitorInfo)
}

// buildOnboardingHandler wraps an onboarding analysis step. On the failure
// that ends the chain for good — a permanent error, or the last allowed
// attempt — it stamps onboarding_error on the web entity so GetOnboardingSteps
// reports ONBOARDING_FAILED instead of leaving the user polling forever.
// Failures with retries remaining stay unmarked: the queue redelivers and the
// step re-runs (both steps are idempotent overwrites).
func buildOnboardingHandler(fn func(ctx context.Context, userId, webEntityId string) error) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p pipeline.StandardPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal onboarding payload: %w", err)
		}
		if err := fn(ctx, msg.UserID, p.WebEntityID); err != nil {
			finalAttempt := msg.Metadata.RetryCount+1 >= msg.Metadata.MaxRetries
			if finalAttempt || errors.Is(err, pipeline.ErrPermanent) {
				if markErr := models.SetWebEntityOnboardingError(ctx, p.WebEntityID, string(msg.ProcessType), err.Error()); markErr != nil {
					log.Error("failed to mark onboarding error on web entity", "error", markErr, "webEntityId", p.WebEntityID, "processType", msg.ProcessType)
				}
			}
			return err
		}
		return nil
	}
}

type ServiceLocator struct {
	UserService              userservice.UserService
	OnboardingService        onboardingService.OnboardingService
	SiteIntelligenceService  sie.SiteIntelligenceService
	ContentGenerationService cge.ContentGenerationService
	SchedulingService        se.SchedulingService
	ContentBridgeService     contentBridge.ContentBridgeService
	StyleReplicationService  stylerep.StyleReplicationService
	AuditService             audit.AuditService
}

type SiteIntelligencePayload struct {
	WebEntityID string `json:"webEntityId"`
}

func buildSiteIntelligenceOrchestratorHandler(locator *ServiceLocator) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p SiteIntelligencePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal site intelligence payload: %w", err)
		}
		return locator.SiteIntelligenceService.Orchestrate(ctx, msg.UserID, p.WebEntityID)
	}
}

func buildSIEHandler(locator *ServiceLocator) func(fn func(ctx context.Context, userId string, metadata sie.SIEMetadata) error) ProcessHandler {
	return func(fn func(ctx context.Context, userId string, metadata sie.SIEMetadata) error) ProcessHandler {
		return func(ctx context.Context, msg MessageEnvelope) error {
			var p pipeline.StandardPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return fmt.Errorf("unmarshal SIE sub-process payload: %w", err)
			}
			metadata := sie.SIEMetadata{
				WebEntityID:        p.WebEntityID,
				WebEntityContextID: p.WebEntityContextID,
				CompetitorURL:      p.CompetitorURL,
			}
			if err := fn(ctx, msg.UserID, metadata); err != nil {
				appendErr := models.AppendErrorData(ctx, p.WebEntityContextID, models.SIEErrorData{
					Step:    string(msg.ProcessType),
					Message: err.Error(),
				})
				if appendErr != nil {
					log.Error("failed to append SIE error data", "error", appendErr, "processType", msg.ProcessType)
				}
				return err
			}
			return nil
		}
	}
}

func buildFunnelClassificationHandler(locator *ServiceLocator) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p sie.FunnelClassificationPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal funnel classification payload: %w", err)
		}
		if err := locator.SiteIntelligenceService.ClassifyFunnelKeywords(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendErrorData(ctx, p.WebEntityContextID, models.SIEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append SIE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func buildManualKeywordEnrichmentHandler(locator *ServiceLocator) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p sie.ManualKeywordPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal manual keyword payload: %w", err)
		}
		if err := locator.SiteIntelligenceService.HandleManualKeywordEnrichment(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendErrorData(ctx, p.WebEntityContextID, models.SIEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append SIE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

// buildUpgradeExpandHandler deliberately does NOT use the generic SIE wrapper:
// that wrapper's AppendErrorData flips the WEC to SIEStatusError, and the
// kickoff's "trial pipeline not finished — retrying" error fires while the
// trial run is still healthy and in flight — flipping it would corrupt that
// run's resume state. Failures are recorded as non-flipping diagnostics and
// returned so SQS retries re-enter the kickoff; once the rewind happens, the
// re-run's stages use the standard SIE error machinery like any base run.
func buildUpgradeExpandHandler(locator *ServiceLocator) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p pipeline.StandardPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal upgrade expand payload: %w", err)
		}
		metadata := sie.SIEMetadata{
			WebEntityID:        p.WebEntityID,
			WebEntityContextID: p.WebEntityContextID,
		}
		if err := locator.SiteIntelligenceService.UpgradeExpand(ctx, msg.UserID, metadata); err != nil {
			if diagErr := models.AppendErrorDiagnostic(ctx, p.WebEntityContextID, models.SIEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			}); diagErr != nil {
				log.Error("failed to append upgrade expand diagnostic", "error", diagErr, "webEntityContextId", p.WebEntityContextID)
			}
			return err
		}
		return nil
	}
}

func buildClusteringHandler(locator *ServiceLocator) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p pipeline.StandardPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal clustering payload: %w", err)
		}
		metadata := sie.SIEMetadata{
			WebEntityID:        p.WebEntityID,
			WebEntityContextID: p.WebEntityContextID,
		}
		if err := locator.SiteIntelligenceService.ClusterKeywords(ctx, msg.UserID, metadata); err != nil {
			appendErr := models.AppendErrorData(ctx, p.WebEntityContextID, models.SIEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append SIE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func registerCGEHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	svc := locator.ContentGenerationService

	registry[cge.ProcessCGEOrchestrate] = buildCGEOrchestrateHandler(svc)
	registry[cge.ProcessCGESerpFetch] = buildCGEStandardHandler(svc.HandleSerpFetch)
	registry[cge.ProcessCGEUrlScrape] = buildCGEUrlScrapeHandler(svc)
	registry[cge.ProcessCGESerpGapAnalysis] = buildCGEStandardHandler(svc.HandleSerpGapAnalysis)
	registry[cge.ProcessCGETavilySearch] = buildCGETavilySearchHandler(svc)
	registry[cge.ProcessCGEYouTubeSearch] = buildCGEStandardHandler(svc.HandleYouTubeSearch)
	registry[cge.ProcessCGEYouTubeTranscript] = buildCGEYouTubeTranscriptHandler(svc)
	registry[cge.ProcessCGEYouTubeSummary] = buildCGEStandardHandler(svc.HandleYouTubeSummary)
	registry[cge.ProcessCGEOutlineGeneration] = buildCGEStandardHandler(svc.HandleOutlineGeneration)
	registry[cge.ProcessCGEArticleGeneration] = buildCGEStandardHandler(svc.HandleArticleGeneration)
	registry[cge.ProcessCGEInternalLinkInsertion] = buildCGEStandardHandler(svc.HandleInternalLinkInsertion)
	registry[cge.ProcessCGEImageGeneration] = buildCGEImageGenerationHandler(svc)
	registry[cge.ProcessCGEImageReplacement] = buildCGEStandardHandler(svc.HandleImageReplacement)
	registry[cge.ProcessCGESchemaGeneration] = buildCGEStandardHandler(svc.HandleSchemaGeneration)
	registry[cge.ProcessCGEMetaAssetsGeneration] = buildCGEStandardHandler(svc.HandleMetaAssetsGeneration)
	registry[cge.ProcessCGEFinalAssembly] = buildCGEStandardHandler(svc.HandleFinalAssembly)
}

func buildCGEOrchestrateHandler(svc cge.ContentGenerationService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGEOrchestratePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE orchestrate payload: %w", err)
		}
		return svc.Orchestrate(ctx, msg.UserID, p)
	}
}

func buildCGEStandardHandler(fn func(ctx context.Context, userId string, payload cge.CGEStandardPayload) error) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGEStandardPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE standard payload: %w", err)
		}
		if err := fn(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendCGEErrorData(ctx, p.WebEntityMasterContextID, models.CGEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append CGE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func buildCGEUrlScrapeHandler(svc cge.ContentGenerationService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGEUrlScrapePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE url scrape payload: %w", err)
		}
		if err := svc.HandleUrlScrape(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendCGEErrorData(ctx, p.WebEntityMasterContextID, models.CGEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append CGE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func buildCGETavilySearchHandler(svc cge.ContentGenerationService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGETavilySearchPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE tavily search payload: %w", err)
		}
		if err := svc.HandleTavilySearch(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendCGEErrorData(ctx, p.WebEntityMasterContextID, models.CGEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append CGE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func buildCGEImageGenerationHandler(svc cge.ContentGenerationService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGEImageGenerationPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE image generation payload: %w", err)
		}
		if err := svc.HandleImageGeneration(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendCGEErrorData(ctx, p.WebEntityMasterContextID, models.CGEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append CGE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}

func registerContentBridgeHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	registry[contentBridge.ProcessContentBridgePublish] = func(ctx context.Context, msg MessageEnvelope) error {
		var p contentBridge.PublishPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal content bridge publish payload: %w", err)
		}
		return locator.ContentBridgeService.HandlePublish(ctx, msg.UserID, p)
	}
}

func registerSchedulingHandlers(registry map[pipeline.ProcessType]ProcessHandler, locator *ServiceLocator) {
	svc := locator.SchedulingService

	registry[se.ProcessSchedulingOrchestrate] = buildSchedulingOrchestrateHandler(svc)
	registry[se.ProcessSchedulingGenerateArticleType] = buildSchedulingArticleStepHandler(svc.GenerateArticleType)
	registry[se.ProcessSchedulingGenerateTitle] = buildSchedulingArticleStepHandler(svc.GenerateTitle)
	registry[se.ProcessSchedulingExtendSchedule] = buildExtendScheduleHandler(svc)
	registry[se.ProcessSchedulingRerunSchedule] = buildRerunScheduleHandler(svc)
}

func buildRerunScheduleHandler(svc se.SchedulingService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p se.SEOrchestratePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal rerun schedule payload: %w", err)
		}
		// MessageID doubles as the rerun resume key: stable across error
		// retries, fresh for each new renewal dispatch.
		return svc.RerunSchedule(ctx, msg.UserID, p, msg.MessageID)
	}
}

func buildExtendScheduleHandler(svc se.SchedulingService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p se.SEOrchestratePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal extend schedule payload: %w", err)
		}
		return svc.ExtendSchedule(ctx, msg.UserID, p)
	}
}

func buildSchedulingOrchestrateHandler(svc se.SchedulingService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p se.SEOrchestratePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal scheduling orchestrate payload: %w", err)
		}
		return svc.Orchestrate(ctx, msg.UserID, p)
	}
}

func buildSchedulingArticleStepHandler(fn func(ctx context.Context, userId string, payload se.SEArticleStepPayload) error) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p se.SEArticleStepPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal scheduling article-step payload: %w", err)
		}
		return fn(ctx, msg.UserID, p)
	}
}

func buildCGEYouTubeTranscriptHandler(svc cge.ContentGenerationService) ProcessHandler {
	return func(ctx context.Context, msg MessageEnvelope) error {
		var p cge.CGEYouTubeTranscriptPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("unmarshal CGE youtube transcript payload: %w", err)
		}
		if err := svc.HandleYouTubeTranscript(ctx, msg.UserID, p); err != nil {
			appendErr := models.AppendCGEErrorData(ctx, p.WebEntityMasterContextID, models.CGEErrorData{
				Step:    string(msg.ProcessType),
				Message: err.Error(),
			})
			if appendErr != nil {
				log.Error("failed to append CGE error data", "error", appendErr, "processType", msg.ProcessType)
			}
			return err
		}
		return nil
	}
}
