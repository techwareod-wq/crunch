package enquiries

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type page[T any] struct {
	Items []T   `json:"items"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

func service(r *http.Request) enquiryService.EnquiryService {
	return config.GetAppContext(r).InternalServices.EnquiryService
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: msg, ErrCode: "invalid"})
}

func actorOf(r *http.Request) domain.Actor {
	u := middleware.GetUserFromContext(r)
	return domain.Actor{UserID: u.ID.Hex(), Email: strings.ToLower(u.Email)}
}

// send maps service errors to HTTP.
func send(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	var ve *enquiryService.ValidationError
	switch {
	case err == nil:
		middleware.SendJSONResponse(w, r, status, v)
	case errors.As(err, &ve):
		badRequest(w, r, ve.Msg)
	case errors.Is(err, enquiryService.ErrStale):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: err.Error(), ErrCode: "version_conflict"})
	case errors.Is(err, enquiryService.ErrListingGone):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusGone, Message: "this listing has been removed", ErrCode: "listing_gone"})
	case errors.Is(err, enquiryService.ErrNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	default:
		middleware.GetLogger(r).Error("enquiry request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
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

// filterOf parses the inbox filter: ?status=, ?assignee= (user id or
// "none"), ?listing= (shortId), ?q=, ?from= / ?to= (to exclusive).
func filterOf(r *http.Request) (models.EnquiryFilter, string) {
	q := r.URL.Query()
	f := models.EnquiryFilter{
		Status:         q.Get("status"),
		ListingShortID: strings.ToLower(strings.TrimSpace(q.Get("listing"))),
		Q:              strings.TrimSpace(q.Get("q")),
	}
	switch f.Status {
	case "", models.EnquiryNew, models.EnquiryContacted, models.EnquiryClosed:
	default:
		return f, "status must be new, contacted or closed"
	}
	switch a := strings.TrimSpace(q.Get("assignee")); a {
	case "":
	case "none":
		f.Unassigned = true
	default:
		id, err := primitive.ObjectIDFromHex(a)
		if err != nil {
			return f, "assignee must be a user id or none"
		}
		f.Assignee = &id
	}
	var ok bool
	if f.From, ok = parseTime(q.Get("from")); !ok {
		return f, "from must be a date (YYYY-MM-DD) or an RFC 3339 time"
	}
	if f.To, ok = parseTime(q.Get("to")); !ok {
		return f, "to must be a date (YYYY-MM-DD) or an RFC 3339 time"
	}
	return f, ""
}

// HandleSubmit serves POST /v1/enquiries: 201 {id}, or 200 {id} when the
// idempotency key was already used.
func HandleSubmit(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(enquiryService.SubmitRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	res, err := service(r).Submit(r.Context(), middleware.GetUserFromContext(r), req)
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	send(w, r, status, res, err)
}

func HandleList(w http.ResponseWriter, r *http.Request) {
	c := config.GetAppContext(r).Config.Values.WarehouseHub.Enquiries
	def, max := c.ListDefault, c.ListMax
	if def <= 0 {
		def = 25
	}
	if max < def {
		max = def
	}
	q := r.URL.Query()
	pg, limit := 1, def
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			badRequest(w, r, "bad page")
			return
		}
		pg = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > max {
			badRequest(w, r, "bad limit")
			return
		}
		limit = n
	}
	f, msg := filterOf(r)
	if msg != "" {
		badRequest(w, r, msg)
		return
	}
	items, total, err := service(r).List(r.Context(), f, pg, limit)
	send(w, r, http.StatusOK, page[models.Enquiry]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}

func HandleDetail(w http.ResponseWriter, r *http.Request) {
	id, err := primitive.ObjectIDFromHex(r.URL.Query().Get("id"))
	if err != nil {
		badRequest(w, r, "id must be an id")
		return
	}
	d, err := service(r).Detail(r.Context(), id)
	send(w, r, http.StatusOK, d, err)
}

func HandleStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(enquiryService.StatusRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	e, err := service(r).SetStatus(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, e, err)
}

func HandleAssign(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(enquiryService.AssignRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	e, err := service(r).Assign(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, e, err)
}

func HandleNote(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(enquiryService.NoteRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	e, err := service(r).AddNote(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, e, err)
}

// HandleExport serves the inbox as CSV (same filters as the list, capped
// at warehousehub.enquiries.exportMax rows).
func HandleExport(w http.ResponseWriter, r *http.Request) {
	f, msg := filterOf(r)
	if msg != "" {
		badRequest(w, r, msg)
		return
	}
	items, err := service(r).Export(r.Context(), f)
	if err != nil {
		send(w, r, http.StatusOK, nil, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="enquiries-`+time.Now().UTC().Format("20060102")+`.csv"`)
	w.WriteHeader(http.StatusOK)
	if err := dto.WriteCSV(w, items); err != nil {
		middleware.GetLogger(r).Error("enquiry export write failed", "error", err)
	}
}
