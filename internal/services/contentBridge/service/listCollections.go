package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ListCollections enumerates the collections reachable with the given
// credentials. Nothing is read from or written to the WebEntity — onboarding
// calls this before the publishing config is saved. authCollection is only
// meaningful for Payload (empty = adapter default "users"); other platforms
// ignore it.
func (s *contentBridgeService) ListCollections(ctx context.Context, platform, apiKey, projectURL, authCollection string) ([]dto.CollectionSummary, error) {
	pub, err := s.registry.Get(platform)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, ErrInvalidCredentials
	}

	cleanURL, err := NormalizeProjectURL(projectURL)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	creds := platforms.PlatformCredentials{
		APIKey:         strings.TrimSpace(apiKey),
		ProjectURL:     cleanURL,
		AuthCollection: strings.TrimSpace(authCollection),
	}
	collections, err := pub.ListCollections(ctx, creds)
	if err != nil {
		log.Error("content_bridge.collections.list",
			"platform", platform,
			"err", err)
		return nil, err
	}

	log.Info("content_bridge.collections.list",
		"platform", platform,
		"collections", len(collections))
	return collections, nil
}

// NormalizeProjectURL validates that s is an absolute http(s) URL and strips
// the query string and fragment — Framer project links are routinely copied
// with `?node=…` editor state appended, which the Server API's connect()
// does not want.
func NormalizeProjectURL(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("project url must be a valid http(s) URL")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
