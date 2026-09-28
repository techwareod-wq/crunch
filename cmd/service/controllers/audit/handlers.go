// Package audit is the /v1/audit controller: tenant run lifecycle (start →
// poll → history) plus the public lead-magnet entry. Entity resolved from
// the active company (house convention); service sentinels map onto
// apperrors via sendServiceError.
package audit

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

// reportsListLimit caps the history listing.
const reportsListLimit = 25

// resolveEntity loads the caller's web entity; a false return means the
// error response was already written (the styleReplication shape).
func resolveEntity(w http.ResponseWriter, r *http.Request) (*models.WebEntity, bool) {
	user := middleware.GetUserFromContext(r)
	active := middleware.GetActiveCompanyFromContext(r)
	logger := middleware.GetLogger(r)

	found, entity, err := models.FindWebEntityForCompany(r.Context(), active.Company.ID, user.ID)
	if err != nil {
		logger.Error("audit: resolving web entity failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return nil, false
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
		return nil, false
	}
	return entity, true
}

// sendServiceError maps the service's typed errors onto the wire.
func sendServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, audit.ErrAuditRunActive):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRunActive)
	case errors.Is(err, audit.ErrAuditFreeLimitReached):
		middleware.SendJSONError(w, r, apperrors.ErrAuditFreeLimitReached)
	case errors.Is(err, audit.ErrAuditCooldown):
		middleware.SendJSONError(w, r, apperrors.ErrAuditCooldown)
	case errors.Is(err, audit.ErrAuditInvalidInput):
		middleware.SendJSONError(w, r, apperrors.ErrAuditInvalidInput)
	case errors.Is(err, audit.ErrAuditInvalidTarget):
		middleware.SendJSONError(w, r, apperrors.ErrAuditInvalidTarget)
	case errors.Is(err, audit.ErrAuditRateLimited):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRateLimited)
	case errors.Is(err, audit.ErrAuditDomainCooldown):
		middleware.SendJSONError(w, r, apperrors.ErrAuditDomainCooldown)
	case errors.Is(err, audit.ErrAuditCapacity):
		middleware.SendJSONError(w, r, apperrors.ErrAuditCapacity)
	case errors.Is(err, audit.ErrAuditReportNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
	case errors.Is(err, audit.ErrAuditRecheckIneligible):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRecheckIneligible)
	case errors.Is(err, audit.ErrAuditRecheckStale):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRecheckStale)
	case errors.Is(err, audit.ErrAuditRecheckCooldown):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRecheckCooldown)
	case errors.Is(err, audit.ErrAuditRecheckLimitReached):
		middleware.SendJSONError(w, r, apperrors.ErrAuditRecheckLimitReached)
	default:
		middleware.GetLogger(r).Error("audit: request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
	}
}

// runView is the poll payload: status + the embedded report when complete +
// the typed error when failed (the transparency contract — the FE renders
// error.reason, never a generic failure).
type runView struct {
	ID           string                 `json:"id"`
	Status       int                    `json:"status"`
	TargetURL    string                 `json:"targetUrl"`
	TargetDomain string                 `json:"targetDomain"`
	SpecVersion  string                 `json:"specVersion,omitempty"`
	CreatedAt    time.Time              `json:"createdAt"`
	CompletedAt  *time.Time             `json:"completedAt,omitempty"`
	Report       *models.AuditReportDoc `json:"report,omitempty"`
	Error        *models.AuditError     `json:"error,omitempty"`

	// Verification overlay (tenant GET only): the latest re-check per check
	// + which check ids are re-checkable — hydrated in one call so the
	// findings panel needs no request fan-out.
	Rechecks    []runRecheckView `json:"rechecks,omitempty"`
	Recheckable []string         `json:"recheckable,omitempty"`
	// NextAuditAvailableAt is the weekly-cadence date the disabled Run
	// button shows ("next audit available <date>"). Server-computed so the
	// FE never re-derives the cooldown rule.
	NextAuditAvailableAt *time.Time `json:"nextAuditAvailableAt,omitempty"`
}

// runRecheckView is one check's latest verdict chip on the run view.
type runRecheckView struct {
	CheckID     string     `json:"checkId"`
	RecheckID   string     `json:"recheckId"`
	Status      int        `json:"status"`
	Verdict     string     `json:"verdict,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

func buildRunView(run *models.AuditRun) *runView {
	return &runView{
		ID:           run.ID.Hex(),
		Status:       run.Status,
		TargetURL:    run.TargetURL,
		TargetDomain: run.TargetDomain,
		SpecVersion:  run.SpecSnapshot.Version,
		CreatedAt:    run.CreatedAt,
		CompletedAt:  run.CompletedAt,
		Report:       run.Report,
		Error:        run.Error,
	}
}

// decorateTenantRunView layers the re-check overlay + the cadence date onto a
// run view. Tenant GET only — the 202 start path and the funnel stay cheap.
func decorateTenantRunView(r *http.Request, run *models.AuditRun, view *runView) {
	appCtx := config.GetAppContext(r)

	if run.Status != models.AuditStatusError {
		cooldownDays := appCtx.Config.Values.Audit.Gating.PaidCooldownDays
		if cooldownDays > 0 {
			next := run.CreatedAt.Add(time.Duration(cooldownDays) * 24 * time.Hour)
			view.NextAuditAvailableAt = &next
		}
	}

	if run.Status != models.AuditStatusComplete {
		return
	}
	view.Recheckable = appCtx.InternalServices.AuditService.RecheckableCheckIDs()
	rechecks, err := models.ListLatestAuditRechecksForRun(r.Context(), run.ID)
	if err != nil {
		middleware.GetLogger(r).Error("audit: recheck overlay load failed", "error", err, "runId", run.ID.Hex())
		return // the report still renders; the overlay just hydrates later
	}
	for i := range rechecks {
		rc := &rechecks[i]
		view.Rechecks = append(view.Rechecks, runRecheckView{
			CheckID:     rc.CheckID,
			RecheckID:   rc.ID.Hex(),
			Status:      rc.Status,
			Verdict:     rc.Verdict,
			CompletedAt: rc.CompletedAt,
		})
	}
}

// reportSummaryView is one history row (id, target, date, score, status).
type reportSummaryView struct {
	ID           string     `json:"id"`
	Status       int        `json:"status"`
	TargetDomain string     `json:"targetDomain"`
	CreatedAt    time.Time  `json:"createdAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	OverallScore *int       `json:"overallScore,omitempty"`
}

// HandleRun serves /v1/audit/run: GET = the dashboard poll, POST = start.
func HandleRun(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		HandleGetRun(w, r)
	case http.MethodPost:
		HandleStartRun(w, r)
	}
}

// startRunRequest is POST /run's optional body (target defaults to the
// entity's website).
type startRunRequest struct {
	TargetURL string `json:"targetUrl"`
}

// HandleStartRun creates a run and dispatches the crawl. 202 — the
// dashboard polls GET /run.
func HandleStartRun(w http.ResponseWriter, r *http.Request) {
	// Method-switched route: decode the optional body by hand (the
	// DeserializeJsonOptional semantics — GET on this path has no body to
	// chain a deserializer for).
	var body startRunRequest
	if decoded, appErr := middleware.DecodeJSONBody[startRunRequest](r.Body); appErr == nil {
		body = decoded
		middleware.SanitizeStruct(&body)
	} else if appErr != apperrors.ErrEmptyRequestBody && appErr != apperrors.ErrNilRequestBody {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	run, err := appCtx.InternalServices.AuditService.StartTenantRun(r.Context(), user, entity, body.TargetURL)
	if err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, buildRunView(run))
}

// HandleGetRun is the dashboard poll: ?id= for a specific run, else the
// entity's newest run (null when the entity never ran).
func HandleGetRun(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}

	if id := r.URL.Query().Get("id"); id != "" {
		found, run, err := models.FindAuditRunByID(r.Context(), id)
		if err != nil || !found {
			middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
			return
		}
		// Tenancy check: the run must belong to the caller's entity.
		if run.WebEntityID == nil || *run.WebEntityID != entity.ID {
			middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
			return
		}
		view := buildRunView(run)
		decorateTenantRunView(r, run, view)
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": view})
		return
	}

	found, run, err := models.FindLatestAuditRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("audit: load latest run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return
	}
	if !found {
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": nil})
		return
	}
	view := buildRunView(run)
	decorateTenantRunView(r, run, view)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": view})
}

// HandleListReports serves the history listing.
func HandleListReports(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	runs, err := models.ListAuditRunsForEntity(r.Context(), entity.ID, reportsListLimit)
	if err != nil {
		middleware.GetLogger(r).Error("audit: list runs failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return
	}
	views := make([]reportSummaryView, 0, len(runs))
	for i := range runs {
		run := &runs[i]
		v := reportSummaryView{
			ID:           run.ID.Hex(),
			Status:       run.Status,
			TargetDomain: run.TargetDomain,
			CreatedAt:    run.CreatedAt,
			CompletedAt:  run.CompletedAt,
		}
		if run.Report != nil {
			score := run.Report.Summary.OverallScore
			v.OverallScore = &score
		}
		views = append(views, v)
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"reports": views})
}
