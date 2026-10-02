package analytics

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
)

type page[T any] struct {
	Items []T   `json:"items"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

func service(r *http.Request) analyticsService.AnalyticsService {
	return config.GetAppContext(r).InternalServices.AnalyticsService
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: msg, ErrCode: "invalid"})
}

func send(w http.ResponseWriter, r *http.Request, v any, err error) {
	var ve *analyticsService.ValidationError
	switch {
	case err == nil:
		middleware.SendJSONResponse(w, r, http.StatusOK, v)
	case errors.As(err, &ve):
		badRequest(w, r, ve.Msg)
	default:
		middleware.GetLogger(r).Error("analytics request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
}

// rangeOf reads ?from= / ?to= (local days, YYYY-MM-DD, inclusive) and
// ?country=.
func rangeOf(r *http.Request) analyticsService.Range {
	q := r.URL.Query()
	return analyticsService.Range{From: q.Get("from"), To: q.Get("to"), Country: q.Get("country")}
}

// intParam reads a positive ?name= bounded by max (def when absent).
func intParam(r *http.Request, name string, def, max int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, false
	}
	return n, true
}

// topLimit reads ?limit= for the query lists.
func topLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	c := config.GetAppContext(r).Config.Values.WarehouseHub.Analytics
	def, max := c.TopDefault, c.TopMax
	if def <= 0 {
		def = 50
	}
	if max < def {
		max = def
	}
	limit, ok := intParam(r, "limit", def, max)
	if !ok {
		badRequest(w, r, "bad limit")
	}
	return limit, ok
}

func HandleOverview(w http.ResponseWriter, r *http.Request) {
	out, err := service(r).Overview(r.Context(), rangeOf(r))
	send(w, r, out, err)
}

func HandleTop(w http.ResponseWriter, r *http.Request) {
	limit, ok := topLimit(w, r)
	if !ok {
		return
	}
	out, err := service(r).Top(r.Context(), rangeOf(r), limit)
	send(w, r, out, err)
}

func HandleZeroResults(w http.ResponseWriter, r *http.Request) {
	limit, ok := topLimit(w, r)
	if !ok {
		return
	}
	out, err := service(r).ZeroResults(r.Context(), rangeOf(r), limit)
	send(w, r, out, err)
}

func HandleConversion(w http.ResponseWriter, r *http.Request) {
	limit, ok := topLimit(w, r)
	if !ok {
		return
	}
	out, err := service(r).Conversion(r.Context(), rangeOf(r), limit)
	send(w, r, out, err)
}

// parseTime reads an RFC 3339 time or a YYYY-MM-DD date (UTC midnight).
func parseTime(raw string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	if t, err := time.Parse(time.DateOnly, raw); err == nil {
		return &t, true
	}
	return nil, false
}

// HandleLog pages the raw search log (≤ 90 days, for debugging the AI):
// ?q= (query substring), ?zero=true, ?from= / ?to= (to exclusive),
// ?page=&limit=.
func HandleLog(w http.ResponseWriter, r *http.Request) {
	c := config.GetAppContext(r).Config.Values.WarehouseHub.Analytics
	def, max := c.LogDefault, c.LogMax
	if def <= 0 {
		def = 50
	}
	if max < def {
		max = def
	}
	pg, ok := intParam(r, "page", 1, int(^uint(0)>>1))
	if !ok {
		badRequest(w, r, "bad page")
		return
	}
	limit, ok := intParam(r, "limit", def, max)
	if !ok {
		badRequest(w, r, "bad limit")
		return
	}
	q := r.URL.Query()
	f := models.SearchEventFilter{Q: strings.TrimSpace(q.Get("q")), Zero: q.Get("zero") == "true"}
	if f.From, ok = parseTime(q.Get("from")); !ok {
		badRequest(w, r, "from must be a date (YYYY-MM-DD) or an RFC 3339 time")
		return
	}
	if f.To, ok = parseTime(q.Get("to")); !ok {
		badRequest(w, r, "to must be a date (YYYY-MM-DD) or an RFC 3339 time")
		return
	}
	items, total, err := service(r).Events(r.Context(), f, pg, limit)
	send(w, r, page[models.SearchEvent]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}
