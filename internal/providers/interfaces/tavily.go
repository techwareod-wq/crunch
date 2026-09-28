package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

type Tavily interface {
	Search(ctx context.Context, req dto.TavilySearchRequest) (*dto.TavilySearchResponse, error)
}
