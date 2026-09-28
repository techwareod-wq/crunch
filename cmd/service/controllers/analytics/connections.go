package analytics

import (
	"errors"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/analytics"
	gscsource "github.com/atharva-ng/crunch/internal/analytics/sources/gsc"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	gscimpl "github.com/atharva-ng/crunch/internal/providers/impl/gsc"
)

// connectionState is one source's connection view for the settings card.
type connectionState struct {
	RequiresConnection bool              `json:"requiresConnection"`
	Status             string            `json:"status,omitempty"`
	ConnectedAt        *time.Time        `json:"connectedAt,omitempty"`
	LastSyncAt         *time.Time        `json:"lastSyncAt,omitempty"`
	LastError          string            `json:"lastError,omitempty"`
	Config             map[string]string `json:"config,omitempty"`
}

type connectionsResponse struct {
	// SAEmail is the service-account address the user adds to their GSC
	// property (the settings card renders it with a copy button).
	SAEmail string                     `json:"saEmail"`
	Sources map[string]connectionState `json:"sources"`
}

// HandleGetConnections returns every registered source's connection state.
func HandleGetConnections(w http.ResponseWriter, r *http.Request) {
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	appCtx := config.GetAppContext(r)

	resp := connectionsResponse{Sources: map[string]connectionState{}}
	if appCtx.GSCProvider != nil {
		resp.SAEmail = appCtx.GSCProvider.ClientEmail()
	}
	for _, src := range analytics.Sources() {
		state := connectionState{RequiresConnection: src.RequiresConnection()}
		if stored, has := req.Entity.Integration(src.Name()); has {
			state.Status = stored.Status
			state.LastError = stored.LastError
			state.Config = stored.Config
			if !stored.ConnectedAt.IsZero() {
				t := stored.ConnectedAt
				state.ConnectedAt = &t
			}
			if !stored.LastSyncAt.IsZero() {
				t := stored.LastSyncAt
				state.LastSyncAt = &t
			}
		}
		resp.Sources[src.Name()] = state
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type connectionRequest struct {
	Source string `json:"source"`
}

// HandleVerifyConnection runs a source's VerifyConnection against the caller's
// entity. On success it stores the connected state and enqueues an IMMEDIATE
// first ingest (audit decision #8) — the dashboard populates within minutes
// instead of waiting for the 07:00 cron; the date-scoped idempotency key is
// the same family the cron uses, so that night's occurrence dedupes cleanly.
func HandleVerifyConnection(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(connectionRequest)
	if !ok || body.Source == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	src, found := analytics.Get(body.Source)
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsSourceUnknown)
		return
	}
	if !src.RequiresConnection() {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	logger := middleware.GetLogger(r)
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	cfg, err := src.VerifyConnection(ctx, req.Entity)
	if err != nil {
		switch {
		case errors.Is(err, gscsource.ErrNoPropertyMatch):
			middleware.SendJSONError(w, r, apperrors.ErrGSCNoPropertyMatch)
		case errors.Is(err, gscimpl.ErrGSCDisabled):
			middleware.SendJSONError(w, r, apperrors.ErrGSCNotConfigured)
		default:
			logger.Error("analytics: verify connection failed", "source", body.Source, "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		}
		return
	}

	state := models.IntegrationState{
		Status:      models.IntegrationStatusConnected,
		ConnectedAt: time.Now().UTC(),
		Config:      cfg,
	}
	prior, hadPrior := req.Entity.Integration(body.Source)
	if hadPrior {
		state.LastSyncAt = prior.LastSyncAt // keep sync history across re-verify
	}
	if err := models.SetWebEntityIntegration(ctx, req.Entity.ID, body.Source, state); err != nil {
		logger.Error("analytics: storing connection state failed", "source", body.Source, "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}

	window := analytics.IngestWindow(src)
	key := cron.UnitKey(cron.JobName("analytics_"+body.Source), window.To, req.Entity.ID.Hex())
	if err := appCtx.InternalServices.Dispatcher.DispatchKeyed(ctx,
		string(analytics.ProcessAnalyticsIngest), user.ID.Hex(), key,
		analytics.IngestPayload{Source: body.Source, WebEntityID: req.Entity.ID.Hex(), Window: window},
	); err != nil {
		// The connection stands; tonight's cron covers the data. Log only.
		logger.Error("analytics: immediate first ingest dispatch failed", "source", body.Source, "error", err)
	}

	// Fresh connect (or reconnect after error/disconnect): pull the source's
	// deep history so the dashboard isn't empty until weeks accumulate. A
	// re-verify while already connected skips it — history is already there.
	// Log-only: the connection stands regardless.
	if _, ok := analytics.BackfillWindow(src); ok && (!hadPrior || prior.Status != models.IntegrationStatusConnected) {
		if _, units, err := analytics.StartBackfill(ctx, appCtx.InternalServices.Dispatcher, src, req.Entity, user.ID.Hex(), ""); err != nil {
			logger.Error("analytics: backfill dispatch failed",
				"source", body.Source, "unitsDispatched", units, "error", err)
		}
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"status": models.IntegrationStatusConnected,
		"config": cfg,
	})
}

// HandleDisconnectConnection flips the source to disconnected. Historical
// facts and raw payloads are deliberately KEPT (requirements Step 3).
func HandleDisconnectConnection(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(connectionRequest)
	if !ok || body.Source == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	if _, found := analytics.Get(body.Source); !found {
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsSourceUnknown)
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	if err := models.SetWebEntityIntegrationFields(r.Context(), req.Entity.ID, body.Source, bson.M{
		"status": models.IntegrationStatusDisconnected,
	}); err != nil {
		middleware.GetLogger(r).Error("analytics: disconnect failed", "source", body.Source, "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{
		"status": models.IntegrationStatusDisconnected,
	})
}
