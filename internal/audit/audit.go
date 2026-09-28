// Package audit owns the SEO/AEO audit engine: a pipeline that crawls a
// target through DataForSEO's OnPage API, fans out data collectors
// (deep-pass HTML features, PSI/CrUX, backlink authority, SERP experience),
// scores 8 categories deterministically against a versioned spec, runs ONE
// Content/E-E-A-T LLM judgment call plus ONE short narrative call, and
// embeds a severity-ranked report with a phased action plan. Two entry
// points: tenant runs (dashboard) and email-gated lead-magnet runs (public
// funnel). See [[SEO Audit Engine]].
package audit

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

// Async process types. Crawl/collect/score ride the primary queue; the two
// LLM stages ride the secondary (token-gated) queue.
const (
	ProcessAuditCrawl        pipeline.ProcessType = "AUDIT_CRAWL"
	ProcessAuditCollect      pipeline.ProcessType = "AUDIT_COLLECT"
	ProcessAuditScore        pipeline.ProcessType = "AUDIT_SCORE"
	ProcessAuditJudgeContent pipeline.ProcessType = "AUDIT_JUDGE_CONTENT"
	ProcessAuditSynthesize   pipeline.ProcessType = "AUDIT_SYNTHESIZE"
	// ProcessAuditRecheck is the per-check re-verification: scoped
	// re-collection + one deterministic check re-run. Single stage, primary
	// queue (LLM checks are excluded from re-check eligibility).
	ProcessAuditRecheck pipeline.ProcessType = "AUDIT_RECHECK"
)

// AuditRunPayload identifies a run for the single-flight stages.
type AuditRunPayload struct {
	RunID string `json:"runId"`
}

// AuditCollectPayload is one collector of the collect fan-out.
type AuditCollectPayload struct {
	RunID       string `json:"runId"`
	CollectorID string `json:"collectorId"`
}

// AuditRecheckPayload identifies a re-check for the AUDIT_RECHECK stage.
type AuditRecheckPayload struct {
	RecheckID string `json:"recheckId"`
}

// Typed errors the synchronous entry points surface for the HTTP layer.
var (
	// ErrAuditRunActive: the entity already has an in-flight audit (409).
	ErrAuditRunActive = errors.New("an audit run is already active")
	// ErrAuditFreeLimitReached: free/trial one-audit-per-website-per-email
	// spent (402 → upsell copy).
	ErrAuditFreeLimitReached = errors.New("free audit for this website already used")
	// ErrAuditCooldown: paid weekly cadence not yet elapsed (429).
	ErrAuditCooldown = errors.New("audit cadence limit reached for this website")
	// ErrAuditInvalidInput: malformed email/body (400).
	ErrAuditInvalidInput = errors.New("invalid audit input")
	// ErrAuditInvalidTarget: the target URL failed SSRF-safe validation (400).
	ErrAuditInvalidTarget = errors.New("invalid audit target URL")
	// ErrAuditRateLimited: lead per-IP cap hit (429).
	ErrAuditRateLimited = errors.New("audit rate limit reached")
	// ErrAuditDomainCooldown: a lead run already audited this domain this
	// week (429 + "recent report exists" copy).
	ErrAuditDomainCooldown = errors.New("this website was audited recently")
	// ErrAuditCapacity: the global daily lead circuit breaker tripped (503).
	ErrAuditCapacity = errors.New("audit capacity reached for today")
	// ErrAuditReportNotFound: no run/report matches the query (404).
	ErrAuditReportNotFound = errors.New("audit report not found")
	// ErrAuditRunNotFailed: admin resume requested on a run that isn't in
	// the terminal Error state (409).
	ErrAuditRunNotFailed = errors.New("audit run is not in a failed state")
	// ErrAuditRecheckIneligible: the check can't be re-verified on its own
	// (LLM judgment, or needs full-crawl/SERP context a spot check can't
	// honestly reproduce) — it's verified on the next audit instead (400).
	ErrAuditRecheckIneligible = errors.New("audit check is not individually re-checkable")
	// ErrAuditRecheckStale: re-check is only offered on the entity's LATEST
	// completed run (409).
	ErrAuditRecheckStale = errors.New("audit re-check only runs against the latest completed audit")
	// ErrAuditRecheckCooldown: the per-check cooldown hasn't elapsed (429).
	ErrAuditRecheckCooldown = errors.New("audit check was just re-verified")
	// ErrAuditRecheckLimitReached: the per-run daily re-check cap (429).
	ErrAuditRecheckLimitReached = errors.New("audit re-check daily limit reached")
)

// Crawl-failure reasons stamped into AuditError.Reason (the transparency
// contract: the FE renders the honest constraint, never a generic failure).
const (
	AuditReasonTargetUnreachable   = "target_unreachable"
	AuditReasonRobotsBlocked       = "robots_blocked"
	AuditReasonRateLimitedByTarget = "rate_limited_by_target"
	AuditReasonTimeout             = "timeout"
)

// AdminResumeResult tells the admin what a resume actually did — which stage
// the run re-entered and, when it couldn't resume in place (artifacts
// TTL-expired, crawl restart), why.
type AdminResumeResult struct {
	ResumedFrom string `json:"resumedFrom"` // crawl | collect | score | judge | synthesize
	Note        string `json:"note,omitempty"`
}

// AuditService is the facade (root-interface + unexported impl in service/,
// the styleReplication shape).
type AuditService interface {
	// StartTenantRun guards the active-run/free/cadence rules, creates the
	// run doc, and dispatches the crawl. Returns the created run.
	StartTenantRun(ctx context.Context, user *models.User, entity *models.WebEntity, targetURL string) (*models.AuditRun, error)
	// StartLeadRun is the Clerk-gated free-audit entry (decision 16 as
	// amended 2026-08-30: no anonymous runs): target validation + the cost
	// caps, then the same pipeline with the lead page cap. Email comes from
	// the authenticated account; the report is account-bound (no poll token).
	StartLeadRun(ctx context.Context, user *models.User, targetURL, clientIP string) (*models.AuditRun, error)

	// AdminResumeRun resumes a failed run from its failed stage (admin
	// recovery): clear the error, bump the retry bookkeeping, reset status
	// to the dead stage's entry status, re-dispatch. Degrades to a full
	// crawl restart when prerequisite artifacts have TTL-expired — never a
	// wedged half-resume.
	AdminResumeRun(ctx context.Context, runID string, adminID primitive.ObjectID) (*AdminResumeResult, error)

	// StartRecheck re-verifies ONE check against fresh, scoped data (§7 of
	// the FE plan): guards eligibility/cooldown/caps, snapshots the before
	// side from the run's outcome, and dispatches AUDIT_RECHECK. The result
	// is a verification overlay — the report and its score never change.
	StartRecheck(ctx context.Context, entity *models.WebEntity, runID, checkID string) (*models.AuditRecheck, error)
	// RecheckableCheckIDs lists the check ids eligible for re-check —
	// computed from the registry at boot, surfaced on the run view so the
	// FE knows which findings carry the button.
	RecheckableCheckIDs() []string

	// Async handlers (SQS).
	HandleCrawl(ctx context.Context, userID string, p AuditRunPayload) error
	HandleCollect(ctx context.Context, userID string, p AuditCollectPayload) error
	// HandleCollectFailure is invoked by the async registry wrapper when a
	// collect message dies for good — a non-critical collector's failure
	// becomes a constraint and the fan-in still completes (§8.3).
	HandleCollectFailure(ctx context.Context, p AuditCollectPayload, msg string) error
	HandleScore(ctx context.Context, userID string, p AuditRunPayload) error
	HandleJudgeContent(ctx context.Context, userID string, p AuditRunPayload) error
	HandleSynthesize(ctx context.Context, userID string, p AuditRunPayload) error
	HandleRecheck(ctx context.Context, userID string, p AuditRecheckPayload) error
}
