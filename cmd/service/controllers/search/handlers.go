package search

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// headerViewerCountry is set by CloudFront when the distribution forwards
// it (D-003).
const headerViewerCountry = "CloudFront-Viewer-Country"

func service(r *http.Request) searchService.SearchService {
	return config.GetAppContext(r).InternalServices.SearchService
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: msg, ErrCode: "invalid"})
}

func sendErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *searchService.ValidationError
	switch {
	case errors.As(err, &ve):
		badRequest(w, r, ve.Msg)
	case errors.Is(err, searchService.ErrNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case errors.Is(err, searchService.ErrGeoUnavailable):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusServiceUnavailable, Message: "location lookup is unavailable — try again", ErrCode: "geocode_unavailable"})
	default:
		middleware.GetLogger(r).Error("search request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
}

// sendCached answers 304 when the client already holds etag.
func sendCached(w http.ResponseWriter, r *http.Request, etag string, v any) {
	if etag != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, v)
}

func viewerOf(r *http.Request) searchService.Viewer {
	u := middleware.GetOptionalUserFromContext(r)
	if u == nil {
		return searchService.Viewer{}
	}
	return searchService.Viewer{UserID: u.ID.Hex(), Staff: u.Role != "" && u.Role != models.RoleUser}
}

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	f, ok := r.Context().Value(middleware.DeserializerContextKey).(domain.SearchFilters)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	resp, err := service(r).Search(r.Context(), f, viewerOf(r))
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleMap: ?country=IN, optional ?filters=<SearchFilters JSON> (without
// it, the cached country view).
func HandleMap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f *domain.SearchFilters
	if raw := strings.TrimSpace(q.Get("filters")); raw != "" {
		var parsed domain.SearchFilters
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			badRequest(w, r, "filters must be a JSON search filter object")
			return
		}
		f = &parsed
	}
	resp, etag, err := service(r).Map(r.Context(), f, q.Get("country"))
	if err != nil {
		sendErr(w, r, err)
		return
	}
	sendCached(w, r, etag, resp)
}

func HandleCatalog(w http.ResponseWriter, r *http.Request) {
	resp, etag := service(r).Catalog(r.URL.Query().Get("country"))
	sendCached(w, r, etag, resp)
}

func HandleResolve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := service(r).Resolve(r.Context(), q.Get("q"), q.Get("country"))
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleWhereAmI returns the viewer's country from CloudFront, else IN
// (India-only launch, D-003).
func HandleWhereAmI(w http.ResponseWriter, r *http.Request) {
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get(headerViewerCountry)))
	if len(c) != 2 {
		c = "IN"
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"country": c})
}
