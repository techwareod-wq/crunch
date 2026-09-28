package service

import (
	"context"

	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// GetBlogStructure fetches the target collection's field schema for the user's
// configured platform. Internal-only (no HTTP route yet) — the publish path
// fetches its own schema, so this exists for a future "test connection" UI.
func (s *contentBridgeService) GetBlogStructure(ctx context.Context, userId string) (dto.BlogSchema, error) {
	we, err := s.resolveWebEntity(ctx, userId)
	if err != nil {
		return dto.BlogSchema{}, err
	}

	pub, err := s.registry.Get(we.Publishing.Platform)
	if err != nil {
		return dto.BlogSchema{}, err
	}

	creds := platforms.PlatformCredentials{
		APIKey:         we.Publishing.ApiKey,
		ProjectURL:     we.Publishing.ProjectURL,
		AuthCollection: we.Publishing.AuthCollection,
	}
	schema, err := pub.FetchSchema(ctx, creds, we.Publishing.CollectionID)
	if err != nil {
		log.Error("content_bridge.schema.fetch",
			"userId", userId,
			"platform", we.Publishing.Platform,
			"collectionId", we.Publishing.CollectionID,
			"err", err)
		return dto.BlogSchema{}, err
	}

	log.Info("content_bridge.schema.fetch",
		"userId", userId,
		"platform", we.Publishing.Platform,
		"collectionId", we.Publishing.CollectionID,
		"fields", len(schema.Fields))
	return schema, nil
}
