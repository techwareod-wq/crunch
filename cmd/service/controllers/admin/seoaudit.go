package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// adminSeoAuditRunsLimit caps the per-user run listing.
const adminSeoAuditRunsLimit = 50

type adminSeoAuditRetryRequest struct {
	RunID string `json:"runId"`
}

// adminSeoAuditRunSummary is one row of the per-user run listing — the
// tenant history shape plus the failure/retry fields ops triage on.
type adminSeoAuditRunSummary struct {
	ID           string             `json:"id"`
	Kind         string             `json:"kind"`
	Status       int                `json:"status"`
	TargetDomain string             `json:"targetDomain"`
	CreatedAt    time.Time          `json:"createdAt"`
	CompletedAt  *time.Time         `json:"completedAt,omitempty"`
	OverallScore *int               `json:"overallScore,omitempty"`
	Error        *models.AuditError `json:"error,omitempty"`
	RetryCount   int                `json:"retryCount,omitempty"`
}

// adminSeoAuditRunView is the full retry-decision view: embedded report,
// error history, fan-in bookkeeping, spec version, retry trail.
type adminSeoAuditRunView struct {
	ID                 string                   `json:"id"`
	Kind               string                   `json:"kind"`
	Status             int                      `json:"status"`
	TargetURL          string                   `json:"targetUrl"`
	TargetDomain       string                   `json:"targetDomain"`
	UserID             string                   `json:"userId,omitempty"`
	PageCap            int                      `json:"pageCap"`
	OnPageTaskID       string                   `json:"onpageTaskId,omitempty"`
	CollectorsExpected []string                 `json:"collectorsExpected,omitempty"`
	CollectorsDone     []string                 `json:"collectorsDone,omitempty"`
	SpecVersion        string                   `json:"specVersion"`
	Report             *models.AuditReportDoc   `json:"report,omitempty"`
	Error              *models.AuditError       `json:"error,omitempty"`
	ErrorHistory       []models.AuditErrorEntry `json:"errorHistory,omitempty"`
	RetryCount         int                      `json:"retryCount,omitempty"`
	LastRetryAt        *time.Time               `json:"lastRetryAt,omitempty"`
	CreatedAt          time.Time                `json:"createdAt"`
	UpdatedAt          time.Time                `json:"updatedAt"`
	CompletedAt        *time.Time               `json:"completedAt,omitempty"`
	// JudgmentMissing marks a COMPLETED run whose content judgment
	// soft-failed — the retry endpoint re-runs judge + synthesize for it
	// (quality uplift 4.1).
	JudgmentMissing bool `json:"judgmentMissing,omitempty"`
}

func buildAdminSeoAuditRunView(run *models.AuditRun) *adminSeoAuditRunView {
	v := &adminSeoAuditRunView{
		ID:                 run.ID.Hex(),
		Kind:               run.Kind,
		Status:             run.Status,
		TargetURL:          run.TargetURL,
		TargetDomain:       run.TargetDomain,
		PageCap:            run.PageCap,
		OnPageTaskID:       run.OnPageTaskID,
		CollectorsExpected: run.CollectorsExpected,
		CollectorsDone:     run.CollectorsDone,
		SpecVersion:        run.SpecSnapshot.Version,
		Report:             run.Report,
		Error:              run.Error,
		ErrorHistory:       run.ErrorHistory,
		RetryCount:         run.RetryCount,
		LastRetryAt:        run.LastRetryAt,
		CreatedAt:          run.CreatedAt,
		UpdatedAt:          run.UpdatedAt,
		CompletedAt:        run.CompletedAt,
	}
	if run.UserID != nil {
		v.UserID = run.UserID.Hex()
	}
	v.JudgmentMissing = run.Status == models.AuditStatusComplete && run.JudgmentOutcome == nil
	return v
}

// HandleAdminSeoAuditRuns lists every audit run for a target user
// (?userId=): runs they started plus runs targeting their entities.
func HandleAdminSeoAuditRuns(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	entityIDs, err := models.FindWebEntityIDsByUserID(ctx, target.ID)
	if err != nil {
		logger.Error("admin seo-audit: entity lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return
	}
	runs, err := models.ListAuditRunsForUser(ctx, target.ID, entityIDs, adminSeoAuditRunsLimit)
	if err != nil {
		logger.Error("admin seo-audit: list runs failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return
	}

	views := make([]adminSeoAuditRunSummary, 0, len(runs))
	for i := range runs {
		run := &runs[i]
		v := adminSeoAuditRunSummary{
			ID:           run.ID.Hex(),
			Kind:         run.Kind,
			Status:       run.Status,
			TargetDomain: run.TargetDomain,
			CreatedAt:    run.CreatedAt,
			CompletedAt:  run.CompletedAt,
			Error:        run.Error,
			RetryCount:   run.RetryCount,
		}
		if run.Report != nil {
			score := run.Report.Summary.OverallScore
			v.OverallScore = &score
		}
		views = append(views, v)
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"runs": views})
}

// HandleAdminSeoAuditRun serves the full run (?id=) — the retry-decision
// view: report, error + errorHistory, collector bookkeeping, retry trail.
func HandleAdminSeoAuditRun(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	found, run, err := models.FindAuditRunByID(r.Context(), id)
	if err != nil || !found {
		middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": buildAdminSeoAuditRunView(run)})
}

// HandleAdminSeoAuditRetry resumes a failed run from its failed stage
// (POST {runId}). 202 with what the resume actually did — the FE confirm
// dialog surfaces resumedFrom + note ("restarts the crawl — re-spends…").
func HandleAdminSeoAuditRetry(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSeoAuditRetryRequest)
	if !ok || req.RunID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	found, run, err := models.FindAuditRunByID(r.Context(), req.RunID)
	if err != nil || !found {
		middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
		return
	}
	// Enrich the audit trail with the run's owner when it has one (free-funnel
	// and tenant runs both stamp UserID; only legacy anonymous leads don't).
	// A deleted owner must not block an ops retry — enrichment is best-effort.
	if run.UserID != nil {
		if _, r2, resolveErr := resolveTargetUser(r, run.UserID.Hex()); resolveErr == nil {
			r = r2
		}
	}

	appCtx := config.GetAppContext(r)
	admin := middleware.GetUserFromContext(r)
	logger := middleware.GetLogger(r)

	result, err := appCtx.InternalServices.AuditService.AdminResumeRun(r.Context(), req.RunID, admin.ID)
	if err != nil {
		switch {
		case errors.Is(err, audit.ErrAuditReportNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
		case errors.Is(err, audit.ErrAuditRunNotFailed):
			middleware.SendJSONError(w, r, apperrors.ErrAuditRunNotFailed)
		default:
			logger.Error("admin seo-audit: resume failed", "error", err, "runId", req.RunID)
			middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		}
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]any{
		"status":      "processing",
		"resumedFrom": result.ResumedFrom,
		"note":        result.Note,
	})
}
