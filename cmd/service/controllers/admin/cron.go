package admin

import (
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/cron"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// cronScheduler is set once in main after BuildCronScheduler (the admin
// package can't ride on AppContext without a config→cron import cycle).
var cronScheduler *cron.Scheduler

// SetCronScheduler wires the scheduler for the run-now endpoint.
func SetCronScheduler(s *cron.Scheduler) { cronScheduler = s }

// adminCronRunRequest names the job to force-run, and optionally the exact
// occurrence to forge (RFC3339). Omitted = the job's latest occurrence, which
// for the sweep-shaped beats is just the current tick window — name an
// occurrence to aim a fixed-minute beat at the local minute it actually fires
// on (03:00Z covers 08:30 in Asia/Kolkata).
type adminCronRunRequest struct {
	Job        string `json:"job"`
	Occurrence string `json:"occurrence,omitempty"`
}

// adminCronRunResponse reports what the forced occurrence did.
type adminCronRunResponse struct {
	Job        string    `json:"job"`
	Occurrence time.Time `json:"occurrence"`
	Candidates int       `json:"candidates"`
	Dispatched int       `json:"dispatched"`
	Capped     int       `json:"capped"`
	Error      string    `json:"error,omitempty"`
}

// HandleAdminCronRun serves POST /v1/admin/cron/run: forges an occurrence and
// runs resolver + dispatch NOW, bypassing the schedule and the claim — how a
// beat is tested without waiting until Thursday 17:00. Send "occurrence" to
// choose which window is forged; without it a fixed-minute beat only resolves
// candidates if the current tick happens to sit on its target minute. Unit
// idempotency keys still apply, so a force-run cannot double-deliver work the
// schedule already delivered.
func HandleAdminCronRun(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminCronRunRequest)
	if !ok || req.Job == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	if cronScheduler == nil {
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	var occ time.Time
	if req.Occurrence != "" {
		parsed, err := time.Parse(time.RFC3339, req.Occurrence)
		if err != nil {
			middleware.GetLogger(r).Error("cron force-run: bad occurrence",
				"job", req.Job, "occurrence", req.Occurrence, "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		occ = parsed
	}

	rec, err := cronScheduler.ForceRunAt(r.Context(), req.Job, occ)
	if err != nil {
		middleware.GetLogger(r).Error("cron force-run failed", "job", req.Job, "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, adminCronRunResponse{
		Job:        rec.Job,
		Occurrence: rec.Occurrence,
		Candidates: rec.Candidates,
		Dispatched: rec.Dispatched,
		Capped:     rec.Capped,
		Error:      rec.Error,
	})
}

// adminCronJobInfo is one registered (enabled) job in the listing.
type adminCronJobInfo struct {
	Job     string `json:"job"`
	Process string `json:"process"`
}

// HandleAdminCronJobs serves GET /v1/admin/cron/jobs — the enabled job names a
// force-run can target (jobs disabled in values don't appear; they have no
// schedule and no resolver wired).
func HandleAdminCronJobs(w http.ResponseWriter, r *http.Request) {
	if cronScheduler == nil {
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	jobs := cronScheduler.Jobs()
	out := make([]adminCronJobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, adminCronJobInfo{Job: string(j.Name), Process: string(j.Process)})
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"jobs": out})
}
