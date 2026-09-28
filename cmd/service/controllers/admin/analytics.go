package admin

import (
	"fmt"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// adminAnalyticsReplayRequest names the source and inclusive [from, to] date
// range to re-normalize; webEntityId omitted = every entity the source's cron
// resolver would cover.
type adminAnalyticsReplayRequest struct {
	Source      string `json:"source"`
	WebEntityID string `json:"webEntityId,omitempty"`
	From        string `json:"from"`
	To          string `json:"to"`
}

// HandleAdminAnalyticsReplay serves POST /v1/admin/analytics/replay (LLD §6):
// mints a replayID, enqueues one unit per (entity, 30-day chunk) through the
// same normalize path as daily ingest, and returns the tracking doc. The range
// is clamped below the live re-fetch window, so a replay can never race the
// daily cron.
func HandleAdminAnalyticsReplay(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminAnalyticsReplayRequest)
	if !ok || req.Source == "" || req.From == "" || req.To == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	var entityID *primitive.ObjectID
	if req.WebEntityID != "" {
		id, err := primitive.ObjectIDFromHex(req.WebEntityID)
		if err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		entityID = &id
	}

	appCtx := config.GetAppContext(r)
	replay, err := analytics.StartReplay(r.Context(), appCtx.InternalServices.Dispatcher,
		req.Source, entityID, req.From, req.To)
	if err != nil {
		middleware.GetLogger(r).Error("admin analytics replay failed to start", "error", err)
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, replay)
}

// HandleAdminAnalyticsReplayStatus serves GET
// /v1/admin/analytics/replay/status?replayId= — the tracking doc.
func HandleAdminAnalyticsReplayStatus(w http.ResponseWriter, r *http.Request) {
	replayID, err := primitive.ObjectIDFromHex(r.URL.Query().Get("replayId"))
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	found, replay, err := models.FindAnalyticsReplayByID(r.Context(), replayID)
	if err != nil {
		middleware.GetLogger(r).Error("admin analytics replay status failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	if !found {
		middleware.SendJSONResponse(w, r, http.StatusNotFound, map[string]string{"error": "replay not found"})
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, replay)
}

// adminAnalyticsBackfillRequest names one (source, entity) to backfill.
type adminAnalyticsBackfillRequest struct {
	Source      string `json:"source"`
	WebEntityID string `json:"webEntityId"`
}

// HandleAdminAnalyticsBackfill serves POST /v1/admin/analytics/backfill: fetch
// the source's full history (values gsc.backfillDays, ~16 months) for one
// entity through the shared ingest process — the manual twin of the
// connect-time auto-backfill, for entities that connected before backfill
// existed or need a gap healed. Idempotent over existing data (raw upserts,
// fact rebuilds).
func HandleAdminAnalyticsBackfill(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminAnalyticsBackfillRequest)
	if !ok || req.Source == "" || req.WebEntityID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	src, found := analytics.Get(req.Source)
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsSourceUnknown)
		return
	}
	ctx := r.Context()
	entityFound, entity, err := models.FindWebEntityByID(ctx, req.WebEntityID)
	if err != nil {
		middleware.GetLogger(r).Error("admin analytics backfill: load web entity failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	if !entityFound {
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
		return
	}
	userID, err := models.RepresentativeUserIDForWebEntity(ctx, entity)
	if err != nil {
		middleware.GetLogger(r).Error("admin analytics backfill: no representative user", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}

	appCtx := config.GetAppContext(r)
	// Nonced keys: an admin re-trigger must actually re-dispatch, never dedupe
	// against an earlier same-day run it is trying to heal.
	nonce := fmt.Sprintf("%d", time.Now().Unix())
	window, units, err := analytics.StartBackfill(ctx, appCtx.InternalServices.Dispatcher, src, entity, userID, nonce)
	if err != nil {
		middleware.GetLogger(r).Error("admin analytics backfill failed to start",
			"unitsDispatched", units, "error", err)
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]any{
		"from":       window.From,
		"to":         window.To,
		"unitsTotal": units,
	})
}

// adminConnectionRow is one entity's integration state for one source.
type adminConnectionRow struct {
	Source      string     `json:"source"`
	WebEntityID string     `json:"webEntityId"`
	CompanyID   string     `json:"companyId,omitempty"`
	WebsiteURL  string     `json:"websiteUrl,omitempty"`
	Status      string     `json:"status"`
	LastError   string     `json:"lastError,omitempty"`
	LastSyncAt  *time.Time `json:"lastSyncAt,omitempty"`
}

// HandleAdminAnalyticsConnections serves GET
// /v1/admin/analytics/connections?status=error — the pull-based ops view of
// broken integrations (deliberately admin-endpoint-only; no comms wiring).
// Default status filter: "error".
func HandleAdminAnalyticsConnections(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = models.IntegrationStatusError
	}
	ctx := r.Context()

	var rows []adminConnectionRow
	for _, src := range analytics.Sources() {
		entities, err := models.FindWebEntitiesWithIntegrationStatus(ctx, src.Name(), status)
		if err != nil {
			middleware.GetLogger(r).Error("admin analytics connections query failed",
				"source", src.Name(), "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
			return
		}
		for _, entity := range entities {
			state, _ := entity.Integration(src.Name())
			row := adminConnectionRow{
				Source:      src.Name(),
				WebEntityID: entity.ID.Hex(),
				WebsiteURL:  entity.WebsiteUrl,
				Status:      state.Status,
				LastError:   state.LastError,
			}
			if !entity.CompanyID.IsZero() {
				row.CompanyID = entity.CompanyID.Hex()
			}
			if !state.LastSyncAt.IsZero() {
				t := state.LastSyncAt
				row.LastSyncAt = &t
			}
			rows = append(rows, row)
		}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"connections": rows})
}
