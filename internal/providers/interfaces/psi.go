package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

// PageSpeedInsights wraps the Google PageSpeed Insights v5 API (CrUX field
// data + Lighthouse lab data). Free, key-only; the audit engine calls it
// directly (no dependency on the analytics layer — scope Constraints).
type PageSpeedInsights interface {
	RunPagespeed(ctx context.Context, req dto.PagespeedRequest) (*dto.PagespeedResponse, error)
}
