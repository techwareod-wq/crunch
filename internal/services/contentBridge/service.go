package contentBridge

import (
	"context"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
)

// ProcessContentBridgePublish is the async process type for publishing a
// generated article to an external CMS. Routed to the primary queue (not the
// rate-limited LLM lane).
const ProcessContentBridgePublish pipeline.ProcessType = "CONTENT_BRIDGE_PUBLISH"

// PublishPayload is the async message body. Small by design (an ID only) so the
// handler rebuilds the BlogPost from persisted state at run time.
type PublishPayload struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
}

type ContentBridgeService interface {
	// ListCollections enumerates the collections reachable with the given
	// credentials. Serves onboarding: the user has typed an API key and
	// project URL but nothing is persisted yet, so credentials arrive with
	// the call instead of being read from the WebEntity. authCollection is
	// Payload-only (empty = adapter default "users").
	ListCollections(ctx context.Context, platform, apiKey, projectURL, authCollection string) ([]dto.CollectionSummary, error)

	// GetBlogStructure fetches the target collection's field schema for the
	// user's configured platform. Internal-only (no HTTP route yet).
	GetBlogStructure(ctx context.Context, userId string) (dto.BlogSchema, error)

	// PostBlogStructure pushes a neutral article to the user's platform.
	// Pure: reads the WebEntity, calls the adapter, returns the result.
	// Does NOT persist publish state — that's HandlePublish's job.
	PostBlogStructure(ctx context.Context, userId string, post dto.BlogPost) (dto.PublishResult, error)

	// HandlePublish is the async worker entry point. Rebuilds the BlogPost from
	// the scheduled article's master context, calls PostBlogStructure, and
	// persists the outcome (success or failure).
	HandlePublish(ctx context.Context, userId string, payload PublishPayload) error
}
