package analytics

import (
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/analytics/metrics"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// HandleSeries serves KindSeries metrics; HandleKPIs serves KindKPI. Same
// envelope: {results: {<name>: MetricResult}}. Unknown metric names 400 with
// the valid list; a metric whose required source isn't connected returns
// {unavailable: true} instead of failing the whole request.
func HandleSeries(w http.ResponseWriter, r *http.Request) {
	serveMetrics(w, r, metrics.KindSeries)
}

func HandleKPIs(w http.ResponseWriter, r *http.Request) {
	serveMetrics(w, r, metrics.KindKPI)
}

func serveMetrics(w http.ResponseWriter, r *http.Request, kind metrics.Kind) {
	names := strings.Split(r.URL.Query().Get("metrics"), ",")
	registry := metrics.Registry()
	var defs []metrics.Def
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		def, ok := registry[name]
		if !ok || def.Kind != kind {
			middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]any{
				"error":        "unknown metric: " + name,
				"validMetrics": metrics.Names(),
			})
			return
		}
		defs = append(defs, def)
	}
	if len(defs) == 0 {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	spec, err := parseRange(r)
	if err != nil {
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	results := map[string]metrics.Result{}
	for _, def := range defs {
		if !sourceAvailable(req.Entity, def.Sources) {
			results[def.Name] = metrics.Result{Unavailable: true}
			continue
		}
		// Each metric clamps the range under its own source's clock (GSC lags
		// 2 Pacific days; domain rating doesn't).
		src, srcOK := analytics.Get(def.Sources[0])
		if !srcOK {
			results[def.Name] = metrics.Result{Unavailable: true}
			continue
		}
		window := materializeRange(spec, src)
		result, err := def.Build(ctx, metrics.Query{
			WebEntityID: req.Entity.ID,
			CompanyID:   req.Entity.CompanyID,
			WECIDs:      req.WECIDs,
			From:        window.From,
			To:          window.To,
			PrevFrom:    window.PrevFrom,
			PrevTo:      window.PrevTo,
		})
		if err != nil {
			logger.Error("analytics: metric build failed", "metric", def.Name, "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
			return
		}
		results[def.Name] = result
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"results": results})
}
