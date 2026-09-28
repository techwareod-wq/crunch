package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
)

const auditReportCollection = "auditReport"

// Audit run status ladder (int + CAS advance, the styleReplication/SIE
// precedent). A run is "active" (blocks concurrent tenant starts) in any
// state below Complete; Error is terminal.
const (
	AuditStatusCreated      = 0
	AuditStatusCrawling     = 1
	AuditStatusCollecting   = 2
	AuditStatusScoring      = 3
	AuditStatusJudging      = 4
	AuditStatusSynthesizing = 5
	AuditStatusComplete     = 6
	AuditStatusError        = 7
)

// ActiveAuditStatuses are the states that block a concurrent tenant start.
var ActiveAuditStatuses = []int{
	AuditStatusCreated,
	AuditStatusCrawling,
	AuditStatusCollecting,
	AuditStatusScoring,
	AuditStatusJudging,
	AuditStatusSynthesizing,
}

// AuditError is the typed terminal failure surfaced to the UI poll. Stage is
// the pipeline stage that died; Reason carries the machine-readable crawl
// failure codes ("target_unreachable", "robots_blocked",
// "rate_limited_by_target", "timeout") when known — the transparency
// contract: report the constraint, never guess site content.
type AuditError struct {
	Stage     string `bson:"stage" json:"stage"`
	Message   string `bson:"message" json:"message"`
	Reason    string `bson:"reason,omitempty" json:"reason,omitempty"`
	Permanent bool   `bson:"permanent" json:"permanent"`
}

// AuditErrorEntry is one terminal failure in the run's error history —
// appended by SetAuditRunError on every terminal stamp, so an admin can see
// whether a retry died the same way. Error stays the LATEST entry (the UI
// poll contract is unchanged).
type AuditErrorEntry struct {
	AuditError `bson:",inline"`
	At         time.Time `bson:"at" json:"at"`
	// Attempt is 1-based and increments per admin retry (attempt N is the
	// failure that ended retry N-1).
	Attempt int `bson:"attempt" json:"attempt"`
}

// AuditCheckOutcome is one check's persisted result — what the score stage
// writes for every deterministic check and the judge stage writes for the LLM
// check. The report is derived from these + the run's spec snapshot, so the
// aggregation is replayable.
type AuditCheckOutcome struct {
	CheckID  string `bson:"check_id" json:"checkId"`
	Category string `bson:"category" json:"category"`
	// Skipped: the check's required artifacts were missing (its allocation
	// renormalizes away within the category).
	Skipped bool `bson:"skipped,omitempty" json:"skipped,omitempty"`
	// HasScore distinguishes score-feeding results from intrinsically
	// findings-only ones (Score == nil).
	HasScore bool           `bson:"has_score" json:"hasScore"`
	Earned   float64        `bson:"earned,omitempty" json:"earned,omitempty"`
	Possible float64        `bson:"possible,omitempty" json:"possible,omitempty"`
	Findings []core.Finding `bson:"findings,omitempty" json:"findings,omitempty"`
	// Evidence is the structured evidence forwarded into the LLM rubric
	// prompt (decision 12).
	Evidence bson.M `bson:"evidence,omitempty" json:"-"`
}

// AuditSummary heads the embedded report.
type AuditSummary struct {
	OverallScore int `bson:"overall_score" json:"overallScore"`
	// UnscoredCategories names categories excluded from the overall (the
	// honesty rule — never silently score what we couldn't measure).
	UnscoredCategories []string       `bson:"unscored_categories,omitempty" json:"unscoredCategories,omitempty"`
	FindingCounts      map[string]int `bson:"finding_counts,omitempty" json:"findingCounts,omitempty"` // severity → count
	PagesCrawled       int            `bson:"pages_crawled" json:"pagesCrawled"`
}

// DomainOverview is the unscored context strip above the categories
// (decision 8) — current snapshot only, no historical pulls.
type DomainOverview struct {
	DomainRating int `bson:"domain_rating" json:"domainRating"`
	// LocationCode/LocationName name the market the ranking numbers were
	// queried against (absent on legacy reports — those queried the US).
	LocationCode     int     `bson:"location_code,omitempty" json:"locationCode,omitempty"`
	LocationName     string  `bson:"location_name,omitempty" json:"locationName,omitempty"`
	KeywordsCount    int64   `bson:"keywords_count" json:"keywordsCount"`
	OrganicETV       float64 `bson:"organic_etv" json:"organicEtv"`
	Pos1             int64   `bson:"pos_1" json:"pos1"`
	Pos2_3           int64   `bson:"pos_2_3" json:"pos2_3"`
	Pos4_10          int64   `bson:"pos_4_10" json:"pos4_10"`
	Pos11_20         int64   `bson:"pos_11_20" json:"pos11_20"`
	Pos21Plus        int64   `bson:"pos_21_plus" json:"pos21Plus"`
	ReferringDomains int64   `bson:"referring_domains" json:"referringDomains"`
	Backlinks        int64   `bson:"backlinks" json:"backlinks"`
	// OnPageScore is DataForSEO's own crawl score ride-along.
	OnPageScore float64 `bson:"onpage_score" json:"onpageScore"`
}

// CategoryReport is one scored category in the embedded report.
type CategoryReport struct {
	ID     string `bson:"id" json:"id"`
	Label  string `bson:"label" json:"label"`
	Weight int    `bson:"weight" json:"weight"`
	Scored bool   `bson:"scored" json:"scored"`
	Score  int    `bson:"score" json:"score"`
	// Reason is the machine-readable why-not when Scored is false
	// (e.g. "psi_unavailable").
	Reason   string         `bson:"reason,omitempty" json:"reason,omitempty"`
	Findings []core.Finding `bson:"findings,omitempty" json:"findings,omitempty"`
}

// ActionPlanItem is one finding placed into a phase, with the quick-win flag
// the code bucketing derives (no LLM involved). Effort/Owner are static
// per-check labels (quality uplift 5.2): effort ∈ {"30 min", "hours",
// "1–2 days", "ongoing"}, owner ∈ {Engineering, Content, Design,
// Founder/Growth}.
type ActionPlanItem struct {
	Finding  core.Finding `bson:"finding" json:"finding"`
	QuickWin bool         `bson:"quick_win" json:"quickWin"`
	Effort   string       `bson:"effort,omitempty" json:"effort,omitempty"`
	Owner    string       `bson:"owner,omitempty" json:"owner,omitempty"`
}

// ActionPlanPhase is one of the four fixed phases.
type ActionPlanPhase struct {
	Title string           `bson:"title" json:"title"`
	Items []ActionPlanItem `bson:"items,omitempty" json:"items,omitempty"`
}

// ActionPlan is the code-bucketed remediation plan plus the optional LLM
// narrative (nil = the narrative call soft-failed; the report still
// completes — decision 17).
type ActionPlan struct {
	Phases    []ActionPlanPhase `bson:"phases" json:"phases"`
	Narrative *string           `bson:"narrative,omitempty" json:"narrative,omitempty"`
}

// StrengthItem is one "what's already right" line (quality uplift 5.1) —
// pure code, derived from near-perfect check outcomes: builds trust and
// tells the reader what to protect against regressions.
type StrengthItem struct {
	CheckID  string `bson:"check_id" json:"checkId"`
	Category string `bson:"category" json:"category"`
	Text     string `bson:"text" json:"text"`
}

// StrategicOpportunity is one LLM-surfaced strategy item (quality uplift
// 4.2). Deliberately NOT a Finding — it carries no falsifiability contract
// and no score impact; it's narrative leverage, not a defect.
type StrategicOpportunity struct {
	Title     string   `bson:"title" json:"title"`
	Detail    string   `bson:"detail" json:"detail"`
	Rationale string   `bson:"rationale,omitempty" json:"rationale,omitempty"`
	Plays     []string `bson:"plays,omitempty" json:"plays,omitempty"`
}

// DriftCategoryDelta is one category's score movement since the previous
// completed audit of the same target.
type DriftCategoryDelta struct {
	ID    string `bson:"id" json:"id"`
	Label string `bson:"label" json:"label"`
	// Previous/Current are the category scores; Delta = Current − Previous.
	Previous int `bson:"previous" json:"previous"`
	Current  int `bson:"current" json:"current"`
	Delta    int `bson:"delta" json:"delta"`
}

// DriftSummary is the "since your last audit" comparison, computed at
// synthesis when a previous completed run exists for the same target.
// Absent on a target's first audit and on legacy reports.
type DriftSummary struct {
	PreviousRunID       primitive.ObjectID   `bson:"previous_run_id" json:"previousRunId"`
	PreviousCompletedAt time.Time            `bson:"previous_completed_at" json:"previousCompletedAt"`
	PreviousScore       int                  `bson:"previous_score" json:"previousScore"`
	CurrentScore        int                  `bson:"current_score" json:"currentScore"`
	OverallDelta        int                  `bson:"overall_delta" json:"overallDelta"`
	CategoryDeltas      []DriftCategoryDelta `bson:"category_deltas,omitempty" json:"categoryDeltas,omitempty"`
	// Resolved/New findings are matched by (check_id, title): resolved =
	// present in the previous report, gone now; new = the reverse. Titles
	// capped — counts carry the full truth.
	ResolvedCount  int      `bson:"resolved_count" json:"resolvedCount"`
	NewCount       int      `bson:"new_count" json:"newCount"`
	ResolvedTitles []string `bson:"resolved_titles,omitempty" json:"resolvedTitles,omitempty"`
	NewTitles      []string `bson:"new_titles,omitempty" json:"newTitles,omitempty"`
}

// AuditReportDoc is the embedded report written on synthesis.
type AuditReportDoc struct {
	Summary        AuditSummary     `bson:"summary" json:"summary"`
	DomainOverview *DomainOverview  `bson:"domain_overview,omitempty" json:"domainOverview,omitempty"`
	Strengths      []StrengthItem   `bson:"strengths,omitempty" json:"strengths,omitempty"`
	Categories     []CategoryReport `bson:"categories" json:"categories"`
	ActionPlan     ActionPlan       `bson:"action_plan" json:"actionPlan"`
	// Drift is the since-last-audit comparison (nil on a first audit).
	Drift *DriftSummary `bson:"drift,omitempty" json:"drift,omitempty"`
	// StrategicOpportunities is the judgment call's strategy section —
	// rendered distinctly from findings (no falsifiability line).
	StrategicOpportunities []StrategicOpportunity `bson:"strategic_opportunities,omitempty" json:"strategicOpportunities,omitempty"`
	// Constraints are the crawl-error-transparency notes (§8.3): what we
	// could NOT measure and why.
	Constraints []core.Finding `bson:"constraints,omitempty" json:"constraints,omitempty"`
}

// AuditRun is one audit run; the report embeds on completion. Collection
// auditReport (one doc per run, camelCase per newest convention).
type AuditRun struct {
	ID     primitive.ObjectID `bson:"_id,omitempty"`
	Kind   string             `bson:"kind"` // core.RunKind: tenant | lead
	Status int                `bson:"status"`

	// Tenant identity (kind=tenant).
	WebEntityID *primitive.ObjectID `bson:"web_entity_id,omitempty"`
	CompanyID   *primitive.ObjectID `bson:"company_id,omitempty"`
	UserID      *primitive.ObjectID `bson:"user_id,omitempty"`

	// Email keys the one-audit-per-website-per-email rule for BOTH kinds:
	// the captured lead email on lead runs, the account email on tenant runs
	// (the free/trial guard spans the two flows). Syntax-checked only
	// (decision 16).
	Email *string `bson:"email,omitempty"`
	// Lead identity (kind=lead) — standalone, no WebEntity.
	PollToken *string `bson:"poll_token,omitempty"` // 32B crypto/rand hex; the shareable report link auth
	ClientIP  *string `bson:"client_ip,omitempty"`  // for the per-IP cap

	TargetURL    string `bson:"target_url"`
	TargetDomain string `bson:"target_domain"` // normalized eTLD+1, rate-limit key

	// LocationCode is the resolved DataForSEO market for ranking lookups
	// (tenant WebEntity setting → ccTLD → homepage lang region), stamped by
	// the crawl stage. 0 = unresolved → collectors default to the US.
	LocationCode int `bson:"location_code,omitempty"`
	// LocationName is the display name for LocationCode ("India"), stamped
	// alongside it so checks can name the market in findings without the
	// catalog. "" on legacy runs.
	LocationName string `bson:"location_name,omitempty"`

	// Crawl bookkeeping.
	OnPageTaskID string `bson:"onpage_task_id,omitempty"`
	PageCap      int    `bson:"page_cap"` // resolved at start (tenant 100 / lead 10)

	// Fan-in bookkeeping — snapshotted at fan-out so registry growth can't
	// wedge in-flight runs.
	CollectorsExpected []string `bson:"collectors_expected,omitempty"`
	CollectorsDone     []string `bson:"collectors_done,omitempty"`

	SpecSnapshot spec.Snapshot `bson:"spec_snapshot"`

	// Stage outputs (score/judge write these; synthesize derives the report).
	CheckOutcomes   []AuditCheckOutcome `bson:"check_outcomes,omitempty"`
	JudgmentOutcome *AuditCheckOutcome  `bson:"judgment_outcome,omitempty"`
	Constraints     []core.Finding      `bson:"constraints,omitempty"`

	Report *AuditReportDoc `bson:"report,omitempty"`

	Error *AuditError `bson:"error,omitempty"`

	// Admin recovery bookkeeping: the full failure trail (Error stays the
	// latest entry) plus who retried and how often.
	ErrorHistory []AuditErrorEntry   `bson:"error_history,omitempty"`
	RetryCount   int                 `bson:"retry_count,omitempty"`
	LastRetryAt  *time.Time          `bson:"last_retry_at,omitempty"`
	RetriedBy    *primitive.ObjectID `bson:"retried_by,omitempty"` // admin user id

	CreatedAt   time.Time  `bson:"created_at"`
	UpdatedAt   time.Time  `bson:"updated_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty"`
}

// EnsureAuditRunIndexes creates the audit run indexes. Idempotent.
func EnsureAuditRunIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		// weekly cadence + listing
		{Keys: bson.D{{Key: "web_entity_id", Value: 1}, {Key: "created_at", Value: -1}}},
		// global domain cap fallback + analytics
		{Keys: bson.D{{Key: "target_domain", Value: 1}, {Key: "created_at", Value: -1}}},
		// lead IP cap
		{Keys: bson.D{{Key: "client_ip", Value: 1}, {Key: "created_at", Value: -1}}},
		// admin per-user visibility
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}},
		// free one-audit-per-website-per-email
		{Keys: bson.D{{Key: "email", Value: 1}, {Key: "target_domain", Value: 1}}},
		// lead poll/share auth
		{
			Keys:    bson.D{{Key: "poll_token", Value: 1}},
			Options: options.Index().SetUnique(true).SetSparse(true),
		},
	}
	if _, err := Collection(auditReportCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure audit run indexes: %w", err)
	}
	return nil
}

func CreateAuditRun(ctx context.Context, run *AuditRun) error {
	now := time.Now()
	run.CreatedAt = now
	run.UpdatedAt = now
	id, err := InsertOne(ctx, auditReportCollection, run)
	if err != nil {
		return fmt.Errorf("create audit run: %w", err)
	}
	run.ID = id
	return nil
}

func FindAuditRunByID(ctx context.Context, id string) (bool, *AuditRun, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid audit run ID: %w", err)
	}
	var run AuditRun
	found, err := FindOne(ctx, auditReportCollection, bson.M{"_id": oid}, &run)
	if err != nil {
		return false, nil, fmt.Errorf("find audit run: %w", err)
	}
	return found, &run, nil
}

// FindLatestLeadAuditRunForUser returns the user's newest FREE (lead-kind)
// run in any state — the funnel page's poll when no run id is supplied. The
// report is account-bound (2026-08-30 amendment: no anonymous runs, no share
// token).
func FindLatestLeadAuditRunForUser(ctx context.Context, userID primitive.ObjectID) (bool, *AuditRun, error) {
	var run AuditRun
	err := Collection(auditReportCollection).FindOne(ctx,
		bson.M{"kind": string(core.RunKindLead), "user_id": userID},
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find latest lead audit run: %w", err)
	}
	return true, &run, nil
}

// FindActiveAuditRunForEntity returns the entity's in-flight run, if any (one
// active audit per entity — the start guard).
func FindActiveAuditRunForEntity(ctx context.Context, entityID primitive.ObjectID) (bool, *AuditRun, error) {
	var run AuditRun
	found, err := FindOne(ctx, auditReportCollection, bson.M{
		"web_entity_id": entityID,
		"status":        bson.M{"$in": ActiveAuditStatuses},
	}, &run)
	if err != nil {
		return false, nil, fmt.Errorf("find active audit run: %w", err)
	}
	return found, &run, nil
}

// FindLatestAuditRunForEntity returns the entity's newest run in ANY state —
// the poll endpoint's read when no run id is supplied.
func FindLatestAuditRunForEntity(ctx context.Context, entityID primitive.ObjectID) (bool, *AuditRun, error) {
	var run AuditRun
	err := Collection(auditReportCollection).FindOne(ctx,
		bson.M{"web_entity_id": entityID},
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find latest audit run: %w", err)
	}
	return true, &run, nil
}

// FindLatestCompletedAuditRunForEntity returns the entity's newest COMPLETED
// run — re-checks are only offered against it (older reports are history).
func FindLatestCompletedAuditRunForEntity(ctx context.Context, entityID primitive.ObjectID) (bool, *AuditRun, error) {
	var run AuditRun
	err := Collection(auditReportCollection).FindOne(ctx,
		bson.M{"web_entity_id": entityID, "status": AuditStatusComplete},
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find latest completed audit run: %w", err)
	}
	return true, &run, nil
}

// FindPreviousCompletedAuditRun returns the newest COMPLETED run for the
// same target that finished BEFORE the given run — the drift baseline. The
// identity scope mirrors how runs are keyed: web entity when the run has
// one, else user + domain (lead runs). False = first audit of this target.
func FindPreviousCompletedAuditRun(ctx context.Context, run *AuditRun) (bool, *AuditRun, error) {
	filter := bson.M{
		"_id":           bson.M{"$ne": run.ID},
		"status":        AuditStatusComplete,
		"target_domain": run.TargetDomain,
	}
	switch {
	case run.WebEntityID != nil:
		filter["web_entity_id"] = *run.WebEntityID
	case run.UserID != nil:
		filter["user_id"] = *run.UserID
	default:
		return false, nil, nil // no stable identity to compare across
	}
	var prev AuditRun
	err := Collection(auditReportCollection).FindOne(ctx, filter,
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&prev)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find previous completed audit run: %w", err)
	}
	return true, &prev, nil
}

// ListAuditRunsForEntity returns the entity's runs newest-first, capped.
func ListAuditRunsForEntity(ctx context.Context, entityID primitive.ObjectID, limit int64) ([]AuditRun, error) {
	cur, err := Collection(auditReportCollection).Find(ctx,
		bson.M{"web_entity_id": entityID},
		options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list audit runs: %w", err)
	}
	defer cur.Close(ctx)
	var runs []AuditRun
	if err := cur.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode audit runs: %w", err)
	}
	return runs, nil
}

// CountAuditRunsForEntitySince counts the entity's runs created at/after
// `since`, EXCLUDING errored runs (a failed attempt doesn't burn the weekly
// cadence slot — styleReplication precedent).
func CountAuditRunsForEntitySince(ctx context.Context, entityID primitive.ObjectID, since time.Time) (int64, error) {
	n, err := Collection(auditReportCollection).CountDocuments(ctx, bson.M{
		"web_entity_id": entityID,
		"created_at":    bson.M{"$gte": since},
		"status":        bson.M{"$ne": AuditStatusError},
	})
	if err != nil {
		return 0, fmt.Errorf("count audit runs for entity: %w", err)
	}
	return n, nil
}

// CountAuditRunsForEmailAndDomain counts non-errored runs for (email,
// domain) across BOTH run kinds — the one-free-audit-per-website-per-email
// rule.
func CountAuditRunsForEmailAndDomain(ctx context.Context, email, domain string) (int64, error) {
	n, err := Collection(auditReportCollection).CountDocuments(ctx, bson.M{
		"email":         email,
		"target_domain": domain,
		"status":        bson.M{"$ne": AuditStatusError},
	})
	if err != nil {
		return 0, fmt.Errorf("count audit runs for email+domain: %w", err)
	}
	return n, nil
}

// CountLeadAuditRunsForIPSince counts lead runs from an IP, INCLUDING errored
// ones — cost was still spent on abuse, so attempts count (LLD §5.1).
func CountLeadAuditRunsForIPSince(ctx context.Context, ip string, since time.Time) (int64, error) {
	n, err := Collection(auditReportCollection).CountDocuments(ctx, bson.M{
		"kind":       string(core.RunKindLead),
		"client_ip":  ip,
		"created_at": bson.M{"$gte": since},
	})
	if err != nil {
		return 0, fmt.Errorf("count lead audit runs for ip: %w", err)
	}
	return n, nil
}

// CountLeadAuditRunsSince is the global lead circuit-breaker count (attempts
// count, same rationale as the IP cap).
func CountLeadAuditRunsSince(ctx context.Context, since time.Time) (int64, error) {
	n, err := Collection(auditReportCollection).CountDocuments(ctx, bson.M{
		"kind":       string(core.RunKindLead),
		"created_at": bson.M{"$gte": since},
	})
	if err != nil {
		return 0, fmt.Errorf("count lead audit runs: %w", err)
	}
	return n, nil
}

// UpdateAuditRun applies a $set document (plus updated_at).
func UpdateAuditRun(ctx context.Context, id string, set bson.M) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit run ID: %w", err)
	}
	set["updated_at"] = time.Now()
	return UpdateOne(ctx, auditReportCollection, bson.M{"_id": oid}, bson.M{"$set": set})
}

// TryAdvanceAuditStatus CASes status from one of `from` to `to`, applying
// extraSet in the same write. Returns whether this caller won the transition
// — the guard that keeps SQS redeliveries from double-running a stage.
func TryAdvanceAuditStatus(ctx context.Context, id string, from []int, to int, extraSet bson.M) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid audit run ID: %w", err)
	}
	set := bson.M{"status": to, "updated_at": time.Now()}
	for k, v := range extraSet {
		set[k] = v
	}
	res, err := Collection(auditReportCollection).UpdateOne(ctx,
		bson.M{"_id": oid, "status": bson.M{"$in": from}},
		bson.M{"$set": set},
	)
	if err != nil {
		return false, fmt.Errorf("advance audit run status: %w", err)
	}
	return res.ModifiedCount > 0, nil
}

// SetAuditRunError flips the run to the terminal error state with the typed
// failure and appends it to the error history (the admin "did my retry die
// the same way?" trail). Reason may be empty for infra failures.
func SetAuditRunError(ctx context.Context, id, stage, message, reason string, permanent bool) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit run ID: %w", err)
	}
	// Attempt = admin retries so far + 1. The extra read is best-effort — a
	// miss just labels the entry attempt 1; the entry itself always lands.
	attempt := 1
	var run AuditRun
	if found, ferr := FindOne(ctx, auditReportCollection, bson.M{"_id": oid}, &run); ferr == nil && found {
		attempt = run.RetryCount + 1
	}
	failure := AuditError{
		Stage:     stage,
		Message:   message,
		Reason:    reason,
		Permanent: permanent,
	}
	return UpdateOne(ctx, auditReportCollection, bson.M{"_id": oid}, bson.M{
		"$set": bson.M{
			"status":     AuditStatusError,
			"error":      failure,
			"updated_at": time.Now(),
		},
		"$push": bson.M{"error_history": AuditErrorEntry{
			AuditError: failure,
			At:         time.Now(),
			Attempt:    attempt,
		}},
	})
}

// TryResumeAuditRun CASes an errored run back into the pipeline for an admin
// resume-from-stage retry: clears the terminal error, bumps the retry
// bookkeeping, and applies extraSet/extraUnset in the same write. Returns
// whether the caller won (the run was still in Error).
func TryResumeAuditRun(ctx context.Context, id string, to int, adminID primitive.ObjectID, extraSet bson.M, extraUnset []string) (bool, error) {
	return tryResumeAuditRunFrom(ctx, id, AuditStatusError, to, adminID, extraSet, extraUnset)
}

// TryResumeCompletedAuditRun re-enters the pipeline from a COMPLETED run —
// the judgment-missing rerun (quality uplift 4.1): the report exists but the
// LLM judgment soft-failed, and re-running judge + synthesize fills it in.
func TryResumeCompletedAuditRun(ctx context.Context, id string, to int, adminID primitive.ObjectID) (bool, error) {
	return tryResumeAuditRunFrom(ctx, id, AuditStatusComplete, to, adminID, nil, nil)
}

func tryResumeAuditRunFrom(ctx context.Context, id string, from, to int, adminID primitive.ObjectID, extraSet bson.M, extraUnset []string) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid audit run ID: %w", err)
	}
	now := time.Now()
	set := bson.M{
		"status":        to,
		"updated_at":    now,
		"last_retry_at": now,
		"retried_by":    adminID,
	}
	for k, v := range extraSet {
		set[k] = v
	}
	unset := bson.M{"error": ""}
	for _, f := range extraUnset {
		unset[f] = ""
	}
	res, err := Collection(auditReportCollection).UpdateOne(ctx,
		bson.M{"_id": oid, "status": from},
		bson.M{"$set": set, "$unset": unset, "$inc": bson.M{"retry_count": 1}},
	)
	if err != nil {
		return false, fmt.Errorf("resume audit run: %w", err)
	}
	return res.ModifiedCount > 0, nil
}

// ListAuditRunsForUser returns every run the user started or that targets one
// of their entities, newest-first, capped — the admin support view.
func ListAuditRunsForUser(ctx context.Context, userID primitive.ObjectID, entityIDs []primitive.ObjectID, limit int64) ([]AuditRun, error) {
	or := []bson.M{{"user_id": userID}}
	if len(entityIDs) > 0 {
		or = append(or, bson.M{"web_entity_id": bson.M{"$in": entityIDs}})
	}
	cur, err := Collection(auditReportCollection).Find(ctx,
		bson.M{"$or": or},
		options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list audit runs for user: %w", err)
	}
	defer cur.Close(ctx)
	var runs []AuditRun
	if err := cur.All(ctx, &runs); err != nil {
		return nil, fmt.Errorf("decode audit runs for user: %w", err)
	}
	return runs, nil
}

// MarkAuditCollectorDone records one collector's completion ($addToSet — a
// redelivered collect message can't double-count) and returns the updated
// doc so the caller can evaluate the fan-in condition.
func MarkAuditCollectorDone(ctx context.Context, id, collectorID string) (*AuditRun, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid audit run ID: %w", err)
	}
	var run AuditRun
	err = Collection(auditReportCollection).FindOneAndUpdate(ctx,
		bson.M{"_id": oid},
		bson.M{
			"$addToSet": bson.M{"collectors_done": collectorID},
			"$set":      bson.M{"updated_at": time.Now()},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&run)
	if err != nil {
		return nil, fmt.Errorf("mark audit collector done: %w", err)
	}
	return &run, nil
}

// SetAuditCheckOutcomes writes the score stage's full deterministic outcome
// set plus the FULL merged constraint list (both $set, so a redelivered
// score run re-writes the identical result instead of double-appending).
func SetAuditCheckOutcomes(ctx context.Context, id string, outcomes []AuditCheckOutcome, allConstraints []core.Finding) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit run ID: %w", err)
	}
	return UpdateOne(ctx, auditReportCollection, bson.M{"_id": oid}, bson.M{
		"$set": bson.M{
			"check_outcomes": outcomes,
			"constraints":    allConstraints,
			"updated_at":     time.Now(),
		},
	})
}

// PullAuditConstraints removes every constraint recorded for one check —
// the judgment-rerun cleanup: a now-successful judgment must not leave its
// stale "judgment unavailable" note in the re-synthesized report.
func PullAuditConstraints(ctx context.Context, id string, checkID string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit run ID: %w", err)
	}
	return UpdateOne(ctx, auditReportCollection, bson.M{"_id": oid}, bson.M{
		"$pull": bson.M{"constraints": bson.M{"check_id": checkID}},
		"$set":  bson.M{"updated_at": time.Now()},
	})
}

// AppendAuditConstraint records one degradation note on the run (§8.3 —
// what we couldn't measure and why).
func AppendAuditConstraint(ctx context.Context, id string, c core.Finding) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid audit run ID: %w", err)
	}
	return UpdateOne(ctx, auditReportCollection, bson.M{"_id": oid}, bson.M{
		"$push": bson.M{"constraints": c},
		"$set":  bson.M{"updated_at": time.Now()},
	})
}
