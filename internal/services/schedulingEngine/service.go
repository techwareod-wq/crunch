package schedulingEngine

import (
	"context"
	"errors"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

var (
	// ErrWebEntityContextNotFound is returned when no web entity context exists for
	// the given web entity / user pair — i.e. the user hasn't completed the
	// site-intelligence stage that produces keywords to schedule against.
	ErrWebEntityContextNotFound = errors.New("web entity context not found")

	// ErrKeywordNotFound is returned when the keyword text supplied to
	// ScheduleArticle does not match any keyword on the user's web entity context.
	ErrKeywordNotFound = errors.New("keyword not found")

	ErrInvalidArticleType = errors.New("invalid article type")

	// ErrInvalidScheduleDate is retained for HTTP mapping of malformed schedule
	// date strings. ScheduleArticle itself accepts past, today, and future dates.
	ErrInvalidScheduleDate = errors.New("schedule date must be in the future")

	// ErrScheduledArticleNotFound is returned when the targeted scheduled article
	// does not exist or does not belong to the requesting user. Missing and
	// unauthorised are collapsed into one error so callers can't probe for the
	// existence of another user's slot.
	ErrScheduledArticleNotFound = errors.New("scheduled article not found")

	// ErrScheduledArticleNotEditable is returned when a title-suggestion or
	// retitle request targets an article whose lifecycle/date puts the relevant
	// field outside its editable window (mirrors the dashboard edit gating).
	ErrScheduledArticleNotEditable = errors.New("scheduled article is not editable")

	// ErrSchedulingNotComplete is returned by DispatchRerunSchedule when the
	// context's previous scheduling run hasn't finished — a rerun appends to a
	// completed calendar, never races an in-flight one.
	ErrSchedulingNotComplete = errors.New("scheduling has not completed for this context")
)

const (
	ProcessSchedulingOrchestrate         pipeline.ProcessType = "SE_ORCHESTRATE"
	ProcessSchedulingGenerateArticleType pipeline.ProcessType = "SE_GENERATE_ARTICLE_TYPE"
	ProcessSchedulingGenerateTitle       pipeline.ProcessType = "SE_GENERATE_TITLE"
	// ProcessSchedulingExtendSchedule appends the full post-upgrade calendar
	// to a trial WEC's existing week-1 rows (upgrade cadence × scheduling
	// window) and finalizes the upgrade. A separate process type because
	// Orchestrate's count>0 no-op guard makes it unable to extend.
	ProcessSchedulingExtendSchedule pipeline.ProcessType = "SE_EXTEND_SCHEDULE"
	// ProcessSchedulingRerunSchedule appends the next full-mode window
	// (articles_per_week × scheduling.weeks) starting the day after the last
	// scheduled slot. Intended to be dispatched on subscription renewal
	// (trigger not wired yet).
	//
	// CONTRACT for the future trigger: dispatch exactly once per renewal
	// period — dedupe upstream on the billing event (e.g. invoice ID). The
	// handler's idempotency covers queue retries of one message, NOT two
	// distinct dispatches: a second dispatch is indistinguishable from the
	// next renewal and appends a second window.
	ProcessSchedulingRerunSchedule pipeline.ProcessType = "SE_RERUN_SCHEDULE"
)

// SEOrchestratePayload is the message dispatched from the SIE clustering edge.
// It carries enough identity to load the WebEntityContext + WebEntity.
type SEOrchestratePayload struct {
	WebEntityID        string `json:"webEntityId"`
	WebEntityContextID string `json:"webEntityContextId"`
}

// SEArticleStepPayload is the per-keyword fan-out payload for the article-type
// and title generation steps. Each scheduled article doc is processed
// independently, so the only field we need is its ID.
type SEArticleStepPayload struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
}

// ScheduleArticleParams carries a single user-initiated calendar addition made
// from the dashboard "New article" flow. The article type is chosen by the
// user; only the title is generated asynchronously.
type ScheduleArticleParams struct {
	WebEntityID  string
	KeywordID    string
	ArticleType  models.ArticleType
	ScheduleDate time.Time
}

type SchedulingService interface {
	Orchestrate(ctx context.Context, userId string, payload SEOrchestratePayload) error
	// ExtendSchedule is the SE_EXTEND_SCHEDULE handler: sets the WebEntity's
	// cadence to the post-upgrade rate, appends slots toward the full window
	// total (existing trial rows untouched), fans out article-type generation
	// for the new rows only, and finalizes the WEC's upgrade (sie_mode=full,
	// upgrade_state=complete). Idempotent — a redelivery recounts and fills
	// only the remainder.
	ExtendSchedule(ctx context.Context, userId string, payload SEOrchestratePayload) error
	// RerunSchedule is the SE_RERUN_SCHEDULE handler: appends one full-mode
	// window (articles_per_week × scheduling.weeks) of new slots starting the
	// day after the current calendar end — or today, when the calendar ended in
	// the past — and fans out article-type generation for the new rows only.
	// rerunKey is the queue MessageID: an error-retry of the same message
	// resumes filling its own window instead of appending a second one.
	RerunSchedule(ctx context.Context, userId string, payload SEOrchestratePayload, rerunKey string) error
	// DispatchRerunSchedule validates the target context (exists, belongs to
	// userId, scheduling complete) and enqueues SE_RERUN_SCHEDULE for it. The
	// synchronous entry point for a rerun — used by the admin console until the
	// renewal-webhook trigger is wired. Missing and unauthorised collapse into
	// ErrWebEntityContextNotFound.
	DispatchRerunSchedule(ctx context.Context, userId, webEntityContextID string) error
	GenerateArticleType(ctx context.Context, userId string, payload SEArticleStepPayload) error
	GenerateTitle(ctx context.Context, userId string, payload SEArticleStepPayload) error
	ScheduleArticle(ctx context.Context, userId string, params ScheduleArticleParams) (*models.ScheduledArticle, *models.Keyword, error)
	GenerateTitleSuggestions(ctx context.Context, userId, scheduledArticleID string) ([]string, error)
	PreviewRetitleForType(ctx context.Context, userId, scheduledArticleID, newArticleType string) (title, reasoning string, err error)
}
