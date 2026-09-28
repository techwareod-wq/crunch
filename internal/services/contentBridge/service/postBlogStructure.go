package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
)

// PostBlogStructure pushes a neutral article to the user's platform. Pure: it
// reads the WebEntity, fetches the collection schema fresh, and calls the
// adapter. It does NOT persist publish state — HandlePublish owns persistence.
func (s *contentBridgeService) PostBlogStructure(ctx context.Context, userId string, post dto.BlogPost) (dto.PublishResult, error) {
	we, err := s.resolveWebEntity(ctx, userId)
	if err != nil {
		return dto.PublishResult{}, err
	}

	pub, err := s.registry.Get(we.Publishing.Platform)
	if err != nil {
		return dto.PublishResult{}, err
	}

	creds := platforms.PlatformCredentials{
		APIKey:         we.Publishing.ApiKey,
		ProjectURL:     we.Publishing.ProjectURL,
		AuthCollection: we.Publishing.AuthCollection,
	}

	// Fetch schema fresh each publish — always correct, no cache invalidation.
	schema, err := pub.FetchSchema(ctx, creds, we.Publishing.CollectionID)
	if err != nil {
		return dto.PublishResult{}, fmt.Errorf("content bridge: fetch schema: %w", err)
	}

	// Create-or-update is decided inside the adapter from post.RemoteItemID.
	return pub.PublishItem(ctx, creds, we.Publishing.CollectionID, post, schema)
}
