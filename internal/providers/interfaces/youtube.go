package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

type YouTube interface {
	SearchVideos(ctx context.Context, req dto.YouTubeSearchRequest) (*dto.YouTubeSearchResponse, error)
	GetTranscript(ctx context.Context, videoID string) (string, error)
}
