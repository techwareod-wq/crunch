package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Seams for tests (mirror listAdminActions in console.go).
var (
	listChangeLog      = models.ListChangeLog
	findChangeLogEntry = models.FindChangeLogEntry
)

// listChangesResponse pages the change log in the shared pagination shape.
type listChangesResponse struct {
	Items []models.ChangeLogEntry `json:"items"`
	Page  int                     `json:"page"`
	Limit int                     `json:"limit"`
	Total int64                   `json:"total"`
}

// HandleAdminListChanges serves GET /v1/admin/changes (audit.read): the change
// log newest-first, without before/after docs. Filters: ?entity= (a
// domain.ChangeEntity), ?entityId=, ?actor= (user id hex or email), ?from= and
// ?to= (RFC 3339, to exclusive), ?page=&limit=. Page bounds reuse the audit
// list's.
func HandleAdminListChanges(w http.ResponseWriter, r *http.Request) {
	pagination := config.GetAppContext(r).Config.Values.Admin.Pagination
	q := r.URL.Query()

	page, limit, ok := parsePageLimit(q.Get("page"), q.Get("limit"), pagination.AuditDefault, pagination.AuditMax)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	f := models.ChangeLogFilter{EntityID: q.Get("entityId")}
	if e := q.Get("entity"); e != "" {
		if !domain.KnownEntity(e) {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		f.Entity = e
	}
	if actor := strings.TrimSpace(q.Get("actor")); actor != "" {
		if primitive.IsValidObjectID(actor) {
			f.ActorUserID = actor
		} else {
			f.ActorEmail = strings.ToLower(actor)
		}
	}
	for _, bound := range []struct {
		raw string
		dst **time.Time
	}{{q.Get("from"), &f.From}, {q.Get("to"), &f.To}} {
		if bound.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, bound.raw)
		if err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		*bound.dst = &t
	}

	items, total, err := listChangeLog(r.Context(), f, page, limit)
	if err != nil {
		middleware.GetLogger(r).Error("admin change log list failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, listChangesResponse{
		Items: items,
		Page:  page,
		Limit: limit,
		Total: total,
	})
}

// HandleAdminChangeDetail serves GET /v1/admin/changes/detail?id= (audit.read):
// one entry with its full before/after documents. The diff is computed
// client-side.
func HandleAdminChangeDetail(w http.ResponseWriter, r *http.Request) {
	id, err := primitive.ObjectIDFromHex(r.URL.Query().Get("id"))
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	found, entry, err := findChangeLogEntry(r.Context(), id)
	if err != nil {
		middleware.GetLogger(r).Error("admin change log detail failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, entry)
}

// parsePageLimit parses 1-based ?page= and ?limit= with the given default and
// cap. ok is false on a malformed or out-of-range value.
func parsePageLimit(rawPage, rawLimit string, defaultLimit, maxLimit int) (page, limit int, ok bool) {
	page, limit = 1, defaultLimit
	if rawPage != "" {
		p, err := strconv.Atoi(rawPage)
		if err != nil || p < 1 {
			return 0, 0, false
		}
		page = p
	}
	if rawLimit != "" {
		l, err := strconv.Atoi(rawLimit)
		if err != nil || l < 1 || l > maxLimit {
			return 0, 0, false
		}
		limit = l
	}
	return page, limit, true
}
