package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

// GSC wraps the Google Search Console API for the analytics engine's "gsc"
// source. Like Mailer, the provider is optional: a deployment without
// GSC_SERVICE_ACCOUNT_JSON gets a real object whose Enabled() is false and
// whose calls fail with a sentinel error — boot never crashes.
type GSC interface {
	// Enabled reports whether a service-account credential is configured.
	Enabled() bool
	// ClientEmail is the service account's email (client_email from the key
	// JSON) — the address users add to their GSC property.
	ClientEmail() string
	// ListSites lists every property the service account has been added to.
	ListSites(ctx context.Context) ([]dto.GSCSite, error)
	// QuerySearchAnalytics runs searchanalytics.query against one property,
	// paginating internally (25k rows/request) up to req.RowLimit total rows.
	QuerySearchAnalytics(ctx context.Context, property string, req dto.GSCQueryRequest) ([]dto.GSCRow, error)
}
