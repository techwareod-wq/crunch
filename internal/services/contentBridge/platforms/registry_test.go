package platforms

import (
	"context"
	"testing"

	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/constants"
)

type stubPublisher struct {
	platform string
}

func (s stubPublisher) Platform() string { return s.platform }
func (s stubPublisher) ListCollections(context.Context, PlatformCredentials) ([]dto.CollectionSummary, error) {
	return nil, nil
}
func (s stubPublisher) FetchSchema(context.Context, PlatformCredentials, string) (dto.BlogSchema, error) {
	return dto.BlogSchema{}, nil
}
func (s stubPublisher) PublishItem(context.Context, PlatformCredentials, string, dto.BlogPost, dto.BlogSchema) (dto.PublishResult, error) {
	return dto.PublishResult{}, nil
}

func TestRegistryGet(t *testing.T) {
	framer := stubPublisher{platform: string(constants.PlatformFramer)}
	r := NewRegistry(framer)

	t.Run("known platform resolves", func(t *testing.T) {
		got, err := r.Get(string(constants.PlatformFramer))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Platform() != string(constants.PlatformFramer) {
			t.Fatalf("got platform %q, want %q", got.Platform(), constants.PlatformFramer)
		}
	})

	t.Run("unknown platform errors", func(t *testing.T) {
		if _, err := r.Get("wordpress"); err == nil {
			t.Fatal("expected error for unregistered platform, got nil")
		}
	})

	t.Run("empty platform errors", func(t *testing.T) {
		if _, err := r.Get(""); err == nil {
			t.Fatal("expected error for empty platform, got nil")
		}
	})
}
