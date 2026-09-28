package audit

import (
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// recheckStartRequest asks for one check's re-verification on one run.
type recheckStartRequest struct {
	RunID   string `json:"runId"`
	CheckID string `json:"checkId"`
}

// recheckView is the re-check poll payload: the verdict overlay plus the
// before/after sides the row's comparison block renders.
type recheckView struct {
	ID          string                   `json:"id"`
	RunID       string                   `json:"runId"`
	CheckID     string                   `json:"checkId"`
	Status      int                      `json:"status"`
	Verdict     string                   `json:"verdict,omitempty"`
	Note        string                   `json:"note,omitempty"`
	Before      *models.AuditRecheckSide `json:"before,omitempty"`
	After       *models.AuditRecheckSide `json:"after,omitempty"`
	CreatedAt   time.Time                `json:"createdAt"`
	CompletedAt *time.Time               `json:"completedAt,omitempty"`
}

func buildRecheckView(rc *models.AuditRecheck) *recheckView {
	return &recheckView{
		ID:          rc.ID.Hex(),
		RunID:       rc.RunID.Hex(),
		CheckID:     rc.CheckID,
		Status:      rc.Status,
		Verdict:     rc.Verdict,
		Note:        rc.Note,
		Before:      rc.Before,
		After:       rc.After,
		CreatedAt:   rc.CreatedAt,
		CompletedAt: rc.CompletedAt,
	}
}

// HandleRecheck serves /v1/audit/recheck: POST = start one check's
// re-verification, GET ?id= = poll it.
func HandleRecheck(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		HandleStartRecheck(w, r)
	case http.MethodGet:
		HandleGetRecheck(w, r)
	}
}

// HandleStartRecheck fires a re-check. 202 — the row polls GET ?id=.
func HandleStartRecheck(w http.ResponseWriter, r *http.Request) {
	// Method-switched route: decode the body by hand (HandleRun precedent).
	body, appErr := middleware.DecodeJSONBody[recheckStartRequest](r.Body)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	middleware.SanitizeStruct(&body)
	if body.RunID == "" || body.CheckID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	appCtx := config.GetAppContext(r)

	rc, err := appCtx.InternalServices.AuditService.StartRecheck(r.Context(), entity, body.RunID, body.CheckID)
	if err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]any{"recheck": buildRecheckView(rc)})
}

// HandleGetRecheck polls one re-check (?id=). Uniform 404 on a tenancy miss.
func HandleGetRecheck(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	found, rc, err := models.FindAuditRecheckByID(r.Context(), id)
	if err != nil || !found || rc.WebEntityID != entity.ID {
		middleware.SendJSONError(w, r, apperrors.ErrAuditReportNotFound)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"recheck": buildRecheckView(rc)})
}
