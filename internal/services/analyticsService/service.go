// Package analyticsService is WarehouseHub search analytics (spec 07): it
// logs every page-1 search (structured and AI) to search_events (90 days,
// D-106), rolls each local day up into search_daily (kept forever), and
// serves the approver dashboards (D-112). Staff searches are logged but
// left out of rollups and dashboards.
package analyticsService

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/analyticsService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// ValidationError is a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// ProcessRollupDaily rolls up yesterday plus any missed day still inside
// the raw window (cron analytics_rollup_daily).
const ProcessRollupDaily pipeline.ProcessType = "analytics.rollup_daily"

// RollupPayload is the analytics.rollup_daily body. Run is the occurrence
// date (it only makes the message key unique).
type RollupPayload struct {
	Run string `json:"run"`
}

// Range is a dashboard date range: local calendar days (YYYY-MM-DD), both
// inclusive. Empty From / To default to the last 30 days. Country empty =
// every country.
type Range struct {
	From    string
	To      string
	Country string
}

// AnalyticsService logs searches and serves the dashboards.
type AnalyticsService interface {
	// Log writes one search event (domain.SearchLogger, wired into 04/05).
	// Errors are logged only.
	domain.SearchLogger

	RollupDaily(ctx context.Context, p RollupPayload) error

	// Dashboards (approver, D-112). Days already rolled up come from
	// search_daily; the rest of the 90-day raw window is computed live.
	Overview(ctx context.Context, r Range) (dto.Overview, error)
	Top(ctx context.Context, r Range, limit int) (dto.TopQueries, error)
	ZeroResults(ctx context.Context, r Range, limit int) (dto.ZeroQueries, error)
	Conversion(ctx context.Context, r Range, limit int) (dto.Conversion, error)
	// Events pages the raw log (≤ 90 days), staff included and flagged.
	Events(ctx context.Context, f models.SearchEventFilter, page, limit int) ([]models.SearchEvent, int64, error)

	// Cleaner drops a deleted account's user id from its searches (D-019).
	Cleaner() accountService.DataCleaner
}
