package config

import (
	"context"
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/tokentracker"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/onboardingService"
	"github.com/atharva-ng/crunch/internal/services/paymentService"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/services/userservice"
)

type contextKey string

const appContextKey contextKey = "appContext"

type LLMProvider struct {
	OpenAI    interfaces.LlmService
	Anthropic interfaces.LlmService
	Gemini    interfaces.LlmService
	Utils     interfaces.LlmUtils
	// DefaultMaxTokens is the standard max-token budget for prompt requests
	// (values.llm.defaultMaxTokens). Services read it instead of a dto const so
	// the budget is tunable without a recompile.
	DefaultMaxTokens int
}

type InternalServices struct {
	UserService              userservice.UserService
	OnboardingService        onboardingService.OnboardingService
	SiteIntelligenceService  siteIntelligenceEngine.SiteIntelligenceService
	ContentGenerationService contentGenerationEngine.ContentGenerationService
	SchedulingService        schedulingEngine.SchedulingService
	ScheduledArticleService  scheduledArticleService.ScheduledArticleService
	PaymentService           paymentService.PaymentService
	AccountService           accountService.AccountService
	ContentBridgeService     contentBridge.ContentBridgeService
	StyleReplicationService  styleReplicationService.StyleReplicationService
	AuditService             audit.AuditService
	LLM                      *LLMProvider
	Dispatcher               interfaces.Dispatcher
}

type AppContext struct {
	Config                 AppConfig
	S3Provider             interfaces.S3
	PaddleProvider         interfaces.PaddleClient
	GSCProvider            interfaces.GSC
	QueueProvider          interfaces.Queue
	SecondaryQueueProvider interfaces.Queue
	IdempotencyStore       interfaces.IdempotencyStore
	TokenTracker           *tokentracker.Tracker
	PlansCache             *entitlements.PlansCache
	RolesCache             *authz.RolesCache
	InternalServices       InternalServices
}

// Middleware returns a middleware function that injects AppContext into the request context.
func (a *AppContext) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), appContextKey, a)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAppContext retrieves the AppContext from the request context.
func GetAppContext(r *http.Request) *AppContext {
	return r.Context().Value(appContextKey).(*AppContext)
}
