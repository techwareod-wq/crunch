package audit

import (
	"net"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// freeStartRequest is the Clerk-gated funnel's entry body — the email comes
// from the authenticated account, never the body (decision 16 as amended
// 2026-08-30: the anonymous lead surface never shipped).
type freeStartRequest struct {
	TargetURL string `json:"targetUrl"`
}

// HandleFreeStart starts the account's free audit. JWT-authed but NOT
// entitlement/company-gated — a fresh sign-up has neither. 202 with the run
// view; the funnel polls GET /v1/audit/free/report.
func HandleFreeStart(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(freeStartRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	run, err := appCtx.InternalServices.AuditService.StartLeadRun(r.Context(), user, body.TargetURL, clientIP(r))
	if err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, buildRunView(run))
}

// HandleFreeReport serves GET /v1/audit/free/report: ?id= for a specific run
// (must belong to the caller — uniform 404 otherwise), else the caller's
// latest free run (null when they never ran one).
func HandleFreeReport(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)

	if id := r.URL.Query().Get("id"); id != "" {
		found, run, err := models.FindAuditRunByID(r.Context(), id)
		if err != nil || !found {
			middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
			return
		}
		if run.UserID == nil || *run.UserID != user.ID {
			middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
			return
		}
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": buildRunView(run)})
		return
	}

	found, run, err := models.FindLatestLeadAuditRunForUser(r.Context(), user.ID)
	if err != nil {
		middleware.GetLogger(r).Error("audit free: load latest run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAuditFailed)
		return
	}
	if !found {
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": nil})
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": buildRunView(run)})
}

// clientIP resolves the caller's IP for the per-IP cap: the first hop of
// X-Forwarded-For when present (the request reaches us through the trusted
// LB), else RemoteAddr. Audit-local until a second consumer appears.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
