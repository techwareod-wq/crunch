package config

import (
	"context"
	"net/http"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/tokentracker"

	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/userservice"
)

type contextKey string

const appContextKey contextKey = "appContext"

type LLMProvider struct {
	Anthropic interfaces.LlmService
	Utils     interfaces.LlmUtils
	// DefaultMaxTokens is the standard max-token budget for prompt requests
	// (values.llm.defaultMaxTokens). Services read it instead of a dto const so
	// the budget is tunable without a recompile.
	DefaultMaxTokens int
}

type InternalServices struct {
	UserService    userservice.UserService
	AccountService accountService.AccountService
	LLM            *LLMProvider
	// ImageGen is the configured image-generation provider
	// (values.apis.imageGen.defaultProvider); nil when its API key is unset.
	ImageGen   interfaces.ImageGenerator
	Dispatcher interfaces.Dispatcher
	// ClerkAccounts is the outbound Clerk Backend API (account deletes).
	ClerkAccounts interfaces.ClerkAccounts
}

type AppContext struct {
	Config     AppConfig
	S3Provider interfaces.S3
	// APIClient is the shared outbound HTTP client (values.apis.httpClient).
	APIClient interfaces.ApiClient
	// Geocoder is nil when GOOGLE_MAPS_API_KEY is unset.
	Geocoder               interfaces.Geocoder
	QueueProvider          interfaces.Queue
	SecondaryQueueProvider interfaces.Queue
	IdempotencyStore       interfaces.IdempotencyStore
	TokenTracker           *tokentracker.Tracker
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
