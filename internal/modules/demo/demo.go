// Package demo is the reference crunch Module. It proves the platform end to
// end and shows the shape every feature follows:
//
//   - an async job (DEMO_SUMMARIZE) on the LLM-gated secondary queue that asks
//     the default LLM for a one-line summary and logs it;
//   - a cron job (demo_heartbeat, dark until enabled in values) that enqueues
//     one DEMO_SUMMARIZE per day;
//   - an admin route (POST /v1/admin/demo/dispatch, cron.manage) that
//     enqueues a DEMO_SUMMARIZE with caller-supplied text.
//
// Delete this package (and its line in cmd/service/modules.go) once a real
// feature exists.
package demo

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/dto"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/pipeline"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
	"github.com/atharva-ng/crunch/internal/util/log"
)

const (
	// ProcessSummarize is the demo async job.
	ProcessSummarize pipeline.ProcessType = "DEMO_SUMMARIZE"
	// JobHeartbeat is the demo cron job (values cron.jobs.demo_heartbeat).
	JobHeartbeat cron.JobName = "demo_heartbeat"

	// systemUserID is the unit owner for platform-initiated work that belongs
	// to no user.
	systemUserID = "system"
	// maxTextLen bounds the admin-supplied text so a demo call can't burn a
	// large prompt.
	maxTextLen          = 4000
	summaryMaxTokens    = 200
	heartbeatCatchUpWin = time.Hour
)

// SummarizePayload is the DEMO_SUMMARIZE message body.
type SummarizePayload struct {
	Text string `json:"text"`
}

// Module is the demo feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the demo module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "demo" }

func (m *Module) RegisterHandlers(reg asynchandler.Registry) {
	reg.Register(ProcessSummarize, asynchandler.Typed(m.summarize))
}

func (m *Module) LLMProcessTypes() []pipeline.ProcessType {
	return []pipeline.ProcessType{ProcessSummarize}
}

func (m *Module) CronJobs() []cron.Job {
	return []cron.Job{{
		Name:    JobHeartbeat,
		Spec:    cron.AtLocal("09:00"),
		Resolve: resolveHeartbeat,
		Process: ProcessSummarize,
		CatchUp: heartbeatCatchUpWin,
	}}
}

func (m *Module) RegisterRoutes(appCtx *config.AppContext) {
	middleware.Handle("/v1/admin/demo/dispatch", http.HandlerFunc(m.handleDispatch)).
		WithAdminAuthorization(authz.PermCronManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[dispatchRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}

// summarize is the DEMO_SUMMARIZE handler.
func (m *Module) summarize(ctx context.Context, userID string, p SummarizePayload) error {
	llm := m.appCtx.InternalServices.LLM.Default(m.appCtx.Config.LLM.DefaultProvider)
	if llm == nil {
		return fmt.Errorf("demo summarize: no LLM provider configured: %w", pipeline.ErrPermanent)
	}
	resp, err := llm.Prompt(ctx, dto.PromptRequest{
		Messages: []dto.Message{
			{Role: dto.RoleSystem, Content: "Summarize the user's text in one short sentence."},
			{Role: dto.RoleUser, Content: p.Text},
		},
		MaxTokens: summaryMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("demo summarize: %w", err)
	}
	log.Info("demo summary", "user_id", userID, "provider", resp.Provider, "summary", strings.TrimSpace(resp.Content))
	return nil
}

// resolveHeartbeat returns one unit per occurrence, owned by the system user.
// The default unit key (job + occurrence + user) dedupes re-runs.
func resolveHeartbeat(ctx context.Context, occ cron.Occurrence) ([]cron.Unit, error) {
	return []cron.Unit{{
		UserID:  systemUserID,
		Payload: SummarizePayload{Text: "crunch heartbeat for " + occ.At.Format(time.DateOnly)},
	}}, nil
}

type dispatchRequest struct {
	Text string `json:"text"`
}

// handleDispatch serves POST /v1/admin/demo/dispatch: enqueues one
// DEMO_SUMMARIZE owned by the calling admin.
func (m *Module) handleDispatch(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dispatchRequest)
	text := strings.TrimSpace(req.Text)
	if !ok || text == "" || len(text) > maxTextLen {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	caller := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)
	if err := appCtx.InternalServices.Dispatcher.Dispatch(r.Context(), string(ProcessSummarize), caller.ID.Hex(), SummarizePayload{Text: text}); err != nil {
		middleware.GetLogger(r).Error("demo dispatch failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrDispatchFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"processType": string(ProcessSummarize)})
}
