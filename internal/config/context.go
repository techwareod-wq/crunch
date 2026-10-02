package config

import (
	"context"
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/tokentracker"

	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/staffinvites"
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

// Default returns the provider named by name ("anthropic" | "openai" |
// "gemini" — LLMConfig.DefaultProvider), falling back to the first configured
// provider when that one has no API key. Nil when no provider is configured.
func (p *LLMProvider) Default(name string) interfaces.LlmService {
	if p == nil {
		return nil
	}
	named := map[string]interfaces.LlmService{
		"anthropic": p.Anthropic,
		"openai":    p.OpenAI,
		"gemini":    p.Gemini,
	}
	if svc := named[name]; svc != nil {
		return svc
	}
	for _, svc := range []interfaces.LlmService{p.Anthropic, p.OpenAI, p.Gemini} {
		if svc != nil {
			return svc
		}
	}
	return nil
}

type InternalServices struct {
	UserService    userservice.UserService
	AccountService accountService.AccountService
	LLM            *LLMProvider
	// ImageGen is the configured image-generation provider
	// (values.apis.imageGen.defaultProvider); nil when its API key is unset.
	ImageGen interfaces.ImageGenerator
	// Mailer sends transactional email; Enabled() is false with no SMTP_HOST.
	Mailer     interfaces.Mailer
	Dispatcher interfaces.Dispatcher
	// ClerkAccounts is the outbound Clerk Backend API (deletes, invitations).
	ClerkAccounts interfaces.ClerkAccounts
	// StaffInvites applies a pending staff invite's role on first sign-in
	// (D-011).
	StaffInvites *staffinvites.Service
}

type AppContext struct {
	Config     AppConfig
	S3Provider interfaces.S3
	// APIClient is the shared outbound HTTP client (values.apis.httpClient).
	APIClient              interfaces.ApiClient
	QueueProvider          interfaces.Queue
	SecondaryQueueProvider interfaces.Queue
	IdempotencyStore       interfaces.IdempotencyStore
	TokenTracker           *tokentracker.Tracker
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
