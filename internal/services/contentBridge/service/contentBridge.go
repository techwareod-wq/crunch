package service

import (
	"context"
	"errors"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/store"
)

// ErrWebEntityNotFound is returned when the user has no web entity to publish
// from. ErrPublishingNotConfigured is returned when the web entity exists but
// has no publishing destination configured (platform / api key).
var (
	ErrWebEntityNotFound       = errors.New("content bridge: web entity not found")
	ErrPublishingNotConfigured = errors.New("content bridge: publishing not configured")
	// ErrInvalidCredentials is returned when the caller-supplied platform or
	// credentials fail validation before any platform call is made.
	ErrInvalidCredentials = errors.New("content bridge: invalid platform credentials")
)

var _ contentBridge.ContentBridgeService = (*contentBridgeService)(nil)

type contentBridgeService struct {
	store    store.Store
	registry platforms.Registry
	// s3 + s3Bucket resolve article image S3 keys into the absolute URLs the CMS
	// fetches (e.g. the thumbnail field). GetS3UrlFromBucketAndFileName is a pure
	// URL template — no network — so buildBlogPost stays synchronous.
	s3       interfaces.S3
	s3Bucket string
}

func NewService(s store.Store, registry platforms.Registry, s3 interfaces.S3, s3Bucket string) contentBridge.ContentBridgeService {
	return &contentBridgeService{store: s, registry: registry, s3: s3, s3Bucket: s3Bucket}
}

// resolveImageURL turns an article image's S3 key into the absolute URL the CMS
// will fetch. Guards a nil s3 (unit tests that don't exercise images) and an
// empty key so callers can pass keys through unconditionally.
func (s *contentBridgeService) resolveImageURL(key string) string {
	if s.s3 == nil || key == "" {
		return ""
	}
	return s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, key)
}

func (s *contentBridgeService) resolveWebEntity(ctx context.Context, userId string) (*models.WebEntity, error) {
	found, we, err := s.store.FindWebEntityByUserID(ctx, userId)
	if err != nil {
		return nil, err
	}
	if !found || we == nil {
		return nil, ErrWebEntityNotFound
	}
	if we.Publishing == nil || we.Publishing.Platform == "" {
		return nil, ErrPublishingNotConfigured
	}
	return we, nil
}
