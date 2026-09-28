package analytics

import (
	"fmt"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// requestEntity is the per-request resolution every handler starts with: the
// active company's web entity (legacy user-owned fallback included) and its
// WEC ids (keyword/article joins).
type requestEntity struct {
	Entity *models.WebEntity
	WECIDs []primitive.ObjectID
}

// resolveEntity loads the caller's web entity; a false return means the error
// response was already written.
func resolveEntity(w http.ResponseWriter, r *http.Request) (*requestEntity, bool) {
	user := middleware.GetUserFromContext(r)
	active := middleware.GetActiveCompanyFromContext(r)
	logger := middleware.GetLogger(r)
	ctx := r.Context()

	found, entity, err := models.FindWebEntityForCompany(ctx, active.Company.ID, user.ID)
	if err != nil {
		logger.Error("analytics: resolving web entity failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return nil, false
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
		return nil, false
	}
	wecIDs, err := models.FindWebEntityContextIDsByWebEntityID(ctx, entity.ID)
	if err != nil {
		logger.Error("analytics: resolving WEC ids failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return nil, false
	}
	return &requestEntity{Entity: entity, WECIDs: wecIDs}, true
}

// rangeSpec is the parsed range request, materialized per source (each source
// clamps to its own settled data — GSC lags 2 Pacific days, domain rating
// doesn't).
type rangeSpec struct {
	preset string // "28d" | "3m" | "12m" | "all" | "custom"
	from   string // custom only
	to     string // custom only
}

// parseRange reads range/from/to query params; default 28d.
func parseRange(r *http.Request) (rangeSpec, error) {
	preset := r.URL.Query().Get("range")
	if preset == "" {
		preset = "28d"
	}
	spec := rangeSpec{preset: preset}
	switch preset {
	case "28d", "3m", "12m", "all":
		return spec, nil
	case "custom":
		spec.from = r.URL.Query().Get("from")
		spec.to = r.URL.Query().Get("to")
		if _, err := time.Parse(analytics.DateLayout, spec.from); err != nil {
			return spec, fmt.Errorf("bad from date %q", spec.from)
		}
		if _, err := time.Parse(analytics.DateLayout, spec.to); err != nil {
			return spec, fmt.Errorf("bad to date %q", spec.to)
		}
		if spec.from > spec.to {
			return spec, fmt.Errorf("from %q after to %q", spec.from, spec.to)
		}
		return spec, nil
	default:
		return spec, fmt.Errorf("unknown range %q (28d|3m|12m|all|custom)", preset)
	}
}

// resolvedWindow is a source-clamped [From, To] plus the equal-length previous
// window for period-over-period deltas ("" for range=all) and the source's
// latest settled date (fixed-window computations: decay, demand capture).
type resolvedWindow struct {
	From, To         string
	PrevFrom, PrevTo string
	Settled          string
}

// materializeRange turns a parsed range into concrete dates under one source's
// clock (LLD §3.1b: all date math goes through the analytics clock).
func materializeRange(spec rangeSpec, src analytics.Source) resolvedWindow {
	settled := analytics.LatestSettledDate(src)
	out := resolvedWindow{Settled: settled, To: settled}

	switch spec.preset {
	case "all":
		out.From = "" // unbounded; no meaningful previous period
		return out
	case "custom":
		clamped := analytics.ClampRange(src, spec.from, spec.to)
		out.From, out.To = clamped.From, clamped.To
	default:
		end, err := time.Parse(analytics.DateLayout, settled)
		if err != nil {
			return out
		}
		var start time.Time
		switch spec.preset {
		case "3m":
			start = end.AddDate(0, -3, 0).AddDate(0, 0, 1)
		case "12m":
			start = end.AddDate(-1, 0, 0).AddDate(0, 0, 1)
		default: // 28d
			start = end.AddDate(0, 0, -27)
		}
		out.From = start.Format(analytics.DateLayout)
	}

	// Previous window: same span, ending the day before From.
	fromT, err1 := time.Parse(analytics.DateLayout, out.From)
	toT, err2 := time.Parse(analytics.DateLayout, out.To)
	if err1 == nil && err2 == nil {
		span := int(toT.Sub(fromT).Hours()/24) + 1
		prevTo := fromT.AddDate(0, 0, -1)
		prevFrom := prevTo.AddDate(0, 0, -(span - 1))
		out.PrevFrom = prevFrom.Format(analytics.DateLayout)
		out.PrevTo = prevTo.Format(analytics.DateLayout)
	}
	return out
}

// gscWindow materializes the range under the GSC source's clock — the tables
// are GSC-only.
func gscWindow(r *http.Request) (resolvedWindow, error) {
	spec, err := parseRange(r)
	if err != nil {
		return resolvedWindow{}, err
	}
	src, ok := analytics.Get("gsc")
	if !ok {
		return resolvedWindow{}, fmt.Errorf("gsc source not registered")
	}
	return materializeRange(spec, src), nil
}

// sourceAvailable reports whether every connection-requiring source in the
// list is connected on the entity.
func sourceAvailable(entity *models.WebEntity, sourceNames []string) bool {
	for _, name := range sourceNames {
		src, ok := analytics.Get(name)
		if !ok {
			return false
		}
		if !src.RequiresConnection() {
			continue
		}
		state, ok := entity.Integration(name)
		if !ok || state.Status != models.IntegrationStatusConnected {
			return false
		}
	}
	return true
}
