package config

import (
	"context"
	"net/http"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"

	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/services/userservice"
)

type contextKey string

const appContextKey contextKey = "appContext"

type InternalServices struct {
	UserService    userservice.UserService
	AccountService accountService.AccountService
	Dispatcher     interfaces.Dispatcher
	// ClerkAccounts is the outbound Clerk Backend API (account deletes).
	ClerkAccounts interfaces.ClerkAccounts
	// WarehouseHub services.
	AttributeService attributeService.AttributeService
	CatalogService   catalogService.CatalogService
	SearchService    searchService.SearchService
	AISearchService  aiSearchService.AISearchService
	EnquiryService   enquiryService.EnquiryService
	AnalyticsService analyticsService.AnalyticsService
}

type AppContext struct {
	Config     AppConfig
	S3Provider interfaces.S3
	// APIClient is the shared outbound HTTP client (values.apis.httpClient).
	APIClient interfaces.ApiClient
	// Geocoder is nil when GOOGLE_MAPS_API_KEY is unset.
	Geocoder         interfaces.Geocoder
	QueueProvider    interfaces.Queue
	IdempotencyStore interfaces.IdempotencyStore
	InternalServices InternalServices
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
