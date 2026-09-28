package platforms

import (
	"context"

	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
)

// PlatformCredentials is everything an adapter needs to authenticate. Passed
// per call so adapters stay stateless and shareable.
type PlatformCredentials struct {
	APIKey string
	// ProjectURL addresses the platform project the key belongs to (Framer:
	// the URL handed to the Server API's connect(); Payload: the server base
	// URL). Empty for platforms that don't need one.
	ProjectURL string
	// AuthCollection is the collection the API key's user lives in (Payload
	// only; defaults to "users" in the adapter when empty).
	AuthCollection string
}

// Publisher is the per-platform contract. It speaks ONLY app-level dto types;
// all native marshaling lives inside the implementation.
type Publisher interface {
	Platform() string
	// ListCollections enumerates the collections the credentials can reach —
	// used during onboarding, before any collection id is configured.
	ListCollections(ctx context.Context, creds PlatformCredentials) ([]dto.CollectionSummary, error)
	FetchSchema(ctx context.Context, creds PlatformCredentials, collectionID string) (dto.BlogSchema, error)
	PublishItem(ctx context.Context, creds PlatformCredentials, collectionID string, post dto.BlogPost, schema dto.BlogSchema) (dto.PublishResult, error)
}
