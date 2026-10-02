package catalog

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type createRequest struct {
	Content models.ListingContent `json:"content"`
}

type revisionRequest struct {
	RevisionID primitive.ObjectID `json:"revisionId"`
	Comment    string             `json:"comment"`
}

type bulkApproveRequest struct {
	RevisionIDs []primitive.ObjectID `json:"revisionIds"`
}

type warehouseRequest struct {
	WarehouseID primitive.ObjectID `json:"warehouseId"`
}

type confirmRequest struct {
	MediaID primitive.ObjectID `json:"mediaId"`
}

type geocodeRequest struct {
	Address models.Address `json:"address"`
}

type page[T any] struct {
	Items []T   `json:"items"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

type createResponse struct {
	Warehouse *models.Warehouse `json:"warehouse"`
	catalogService.RevisionResult
}

func service(r *http.Request) catalogService.CatalogService {
	return config.GetAppContext(r).InternalServices.CatalogService
}

// --- helpers ---

func body[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	v, ok := r.Context().Value(middleware.DeserializerContextKey).(T)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
	}
	return v, ok
}

func actorOf(r *http.Request) domain.Actor {
	u := middleware.GetUserFromContext(r)
	if u == nil {
		return domain.SystemActor
	}
	return domain.Actor{UserID: u.ID.Hex(), Email: strings.ToLower(u.Email)}
}

func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: msg, ErrCode: "invalid"})
}

func queryID(w http.ResponseWriter, r *http.Request, name string) (primitive.ObjectID, bool) {
	id, err := primitive.ObjectIDFromHex(r.URL.Query().Get(name))
	if err != nil {
		badRequest(w, r, name+" must be an id")
		return id, false
	}
	return id, true
}

func requireID(w http.ResponseWriter, r *http.Request, id primitive.ObjectID, name string) bool {
	if id.IsZero() {
		badRequest(w, r, name+" is required")
		return false
	}
	return true
}

// pageOf parses 1-based ?page= and ?limit=.
func pageOf(r *http.Request, def, max int) (int, int, bool) {
	q := r.URL.Query()
	page, limit := 1, def
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, false
		}
		page = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > max {
			return 0, 0, false
		}
		limit = n
	}
	return page, limit, true
}

func listBounds(r *http.Request) (int, int) {
	c := config.GetAppContext(r).Config.Values.WarehouseHub.Catalog
	def, max := c.ListDefault, c.ListMax
	if def <= 0 {
		def = 25
	}
	if max < def {
		max = def
	}
	return def, max
}

// sendErr maps service errors to HTTP.
func sendErr(w http.ResponseWriter, r *http.Request, err error) {
	var (
		ve *catalogService.ValidationError
		se *catalogService.StateError
		sb *catalogService.SubmitBlockedError
	)
	switch {
	case errors.As(err, &ve):
		badRequest(w, r, ve.Msg)
	case errors.As(err, &sb):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: "fix these before submitting",
			ErrCode: "submit_blocked", Data: map[string]any{"problems": sb.Problems}})
	case errors.As(err, &se):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: se.Msg, ErrCode: se.Code})
	case errors.Is(err, catalogService.ErrNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	default:
		middleware.GetLogger(r).Error("catalog request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
}

func send(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, status, v)
}

// --- warehouses ---

func HandleList(w http.ResponseWriter, r *http.Request) {
	def, max := listBounds(r)
	pg, limit, ok := pageOf(r, def, max)
	if !ok {
		badRequest(w, r, "bad page or limit")
		return
	}
	q := r.URL.Query()
	f := models.WarehouseFilter{Status: q.Get("status"), Q: strings.TrimSpace(q.Get("q")), City: strings.TrimSpace(q.Get("city")), NeedsInfo: q.Get("needsInfo") == "true"}
	switch f.Status {
	case "", models.WarehouseUnpublished, models.WarehouseLive, models.WarehouseArchived:
	default:
		badRequest(w, r, "status must be unpublished, live or archived")
		return
	}
	items, total, err := service(r).ListWarehouses(r.Context(), f, pg, limit)
	send(w, r, http.StatusOK, page[models.Warehouse]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}

func HandleDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	detail, err := service(r).WarehouseDetail(r.Context(), id)
	send(w, r, http.StatusOK, detail, err)
}

func HandleCreate(w http.ResponseWriter, r *http.Request) {
	req, _ := r.Context().Value(middleware.DeserializerContextKey).(createRequest)
	wh, res, err := service(r).Create(r.Context(), actorOf(r), req.Content)
	send(w, r, http.StatusCreated, createResponse{Warehouse: wh, RevisionResult: res}, err)
}

func warehouseAction(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, a domain.Actor, id primitive.ObjectID) error) {
	req, ok := body[warehouseRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	err := fn(r.Context(), actorOf(r), req.WarehouseID)
	send(w, r, http.StatusOK, map[string]any{"warehouseId": req.WarehouseID.Hex()}, err)
}

func HandleArchive(w http.ResponseWriter, r *http.Request) {
	warehouseAction(w, r, service(r).Archive)
}

func HandleDelete(w http.ResponseWriter, r *http.Request) {
	warehouseAction(w, r, service(r).Delete)
}

func HandleRestore(w http.ResponseWriter, r *http.Request) {
	req, ok := body[warehouseRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := service(r).Restore(r.Context(), actorOf(r), req.WarehouseID)
	send(w, r, http.StatusCreated, res, err)
}

// --- revisions ---

func HandleRevisionDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	rev, err := service(r).GetRevision(r.Context(), id)
	send(w, r, http.StatusOK, rev, err)
}

func HandleSave(w http.ResponseWriter, r *http.Request) {
	req, ok := body[catalogService.SaveRequest](w, r)
	if !ok || !requireID(w, r, req.RevisionID, "revisionId") {
		return
	}
	res, err := service(r).Save(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, res, err)
}

func HandleOpen(w http.ResponseWriter, r *http.Request) {
	req, ok := body[warehouseRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := service(r).Open(r.Context(), actorOf(r), req.WarehouseID)
	send(w, r, http.StatusCreated, res, err)
}

func revisionAction(w http.ResponseWriter, r *http.Request, fn func(req revisionRequest) (catalogService.RevisionResult, error)) {
	req, ok := body[revisionRequest](w, r)
	if !ok || !requireID(w, r, req.RevisionID, "revisionId") {
		return
	}
	res, err := fn(req)
	send(w, r, http.StatusOK, res, err)
}

func HandleSubmit(w http.ResponseWriter, r *http.Request) {
	revisionAction(w, r, func(req revisionRequest) (catalogService.RevisionResult, error) {
		return service(r).Submit(r.Context(), actorOf(r), req.RevisionID, "")
	})
}

func HandleWithdraw(w http.ResponseWriter, r *http.Request) {
	u := middleware.GetUserFromContext(r)
	if !authz.Has(u, authz.PermEditor) && !authz.Has(u, authz.PermApprover) {
		middleware.SendJSONError(w, r, apperrors.ErrAdminAccessRequired)
		return
	}
	revisionAction(w, r, func(req revisionRequest) (catalogService.RevisionResult, error) {
		return service(r).Withdraw(r.Context(), actorOf(r), req.RevisionID)
	})
}

func HandleDiscard(w http.ResponseWriter, r *http.Request) {
	revisionAction(w, r, func(req revisionRequest) (catalogService.RevisionResult, error) {
		return service(r).Discard(r.Context(), actorOf(r), req.RevisionID)
	})
}

func HandleApprove(w http.ResponseWriter, r *http.Request) {
	revisionAction(w, r, func(req revisionRequest) (catalogService.RevisionResult, error) {
		return service(r).Approve(r.Context(), actorOf(r), req.RevisionID, "")
	})
}

func HandleReject(w http.ResponseWriter, r *http.Request) {
	revisionAction(w, r, func(req revisionRequest) (catalogService.RevisionResult, error) {
		return service(r).Reject(r.Context(), actorOf(r), req.RevisionID, req.Comment)
	})
}

func HandleBulkApprove(w http.ResponseWriter, r *http.Request) {
	req, ok := body[bulkApproveRequest](w, r)
	if !ok {
		return
	}
	if len(req.RevisionIDs) == 0 {
		badRequest(w, r, "revisionIds is required")
		return
	}
	batchID, items, err := service(r).BulkApprove(r.Context(), actorOf(r), req.RevisionIDs)
	send(w, r, http.StatusOK, map[string]any{"batchId": batchID, "items": items}, err)
}

func HandleQueue(w http.ResponseWriter, r *http.Request) {
	def, max := listBounds(r)
	pg, limit, ok := pageOf(r, def, max)
	if !ok {
		badRequest(w, r, "bad page or limit")
		return
	}
	items, total, err := service(r).ReviewQueue(r.Context(), pg, limit)
	send(w, r, http.StatusOK, page[models.WarehouseRevision]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}

func HandleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "warehouseId")
	if !ok {
		return
	}
	items, err := service(r).RevisionHistory(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"items": items}, err)
}

// --- geocode + media ---

func HandleGeocodePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := body[geocodeRequest](w, r)
	if !ok {
		return
	}
	loc, err := service(r).GeocodePreview(r.Context(), req.Address)
	send(w, r, http.StatusOK, loc, err)
}

func HandleMediaList(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "warehouseId")
	if !ok {
		return
	}
	items, err := service(r).ListMedia(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"items": items}, err)
}

func HandleUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := body[catalogService.UploadRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := service(r).MediaUploadURL(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, res, err)
}

func HandleConfirm(w http.ResponseWriter, r *http.Request) {
	req, ok := body[confirmRequest](w, r)
	if !ok || !requireID(w, r, req.MediaID, "mediaId") {
		return
	}
	md, err := service(r).ConfirmMedia(r.Context(), req.MediaID)
	send(w, r, http.StatusOK, md, err)
}

func HandleLink(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	url, err := service(r).MediaLink(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"url": url}, err)
}

// --- public ---

func HandlePublicListing(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	res, err := service(r).PublicListing(r.Context(), slug)
	switch {
	case errors.Is(err, catalogService.ErrNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case err != nil:
		sendErr(w, r, err)
	case res.RedirectTo != "":
		middleware.SendJSONResponse(w, r, http.StatusMovedPermanently, map[string]string{"redirectTo": res.RedirectTo})
	case res.IsGone:
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusGone, Message: "this listing has been removed",
			ErrCode: "gone", Data: map[string]any{"nearby": res.Nearby}})
	default:
		middleware.SendJSONResponse(w, r, http.StatusOK, res.Listing)
	}
}

func HandlePublicSlugs(withCover bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pg := 1
		if v := r.URL.Query().Get("page"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				badRequest(w, r, "bad page")
				return
			}
			pg = n
		}
		items, err := service(r).PublicSlugs(r.Context(), pg, withCover)
		send(w, r, http.StatusOK, map[string]any{"items": items, "page": pg}, err)
	}
}
