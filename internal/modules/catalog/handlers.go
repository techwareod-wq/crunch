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
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type createRequest struct {
	Content domain.Content `json:"content"`
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
	Address domain.Address `json:"address"`
}

type page[T any] struct {
	Items []T   `json:"items"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

type detailResponse struct {
	Warehouse    *domain.Warehouse `json:"warehouse"`
	OpenRevision *domain.Revision  `json:"openRevision"`
	Preview      *Preview          `json:"preview,omitempty"`
	Media        []domain.Media    `json:"media"`
	History      []domain.Revision `json:"history"`
}

type createResponse struct {
	Warehouse *domain.Warehouse `json:"warehouse"`
	RevisionResult
}

func (m *Module) RegisterRoutes(appCtx *config.AppContext) {
	admin := func(path string, h http.HandlerFunc, perm authz.Permission, method string, body func(http.Handler) http.Handler) {
		var required []authz.Permission
		if perm != authz.PermAdmin {
			required = append(required, perm)
		}
		p := middleware.Handle(path, h).WithAdminAuthorization(required...).WithJWTAuthentication()
		if body != nil {
			p = p.With(body)
		}
		p.WithMethods(method).With(appCtx.Middleware()).AllowCORS().WithLogEnabled()
	}
	get, post := http.MethodGet, http.MethodPost
	read, edit, approve := authz.PermAdmin, authz.PermEditor, authz.PermApprover

	admin("/v1/admin/warehouses", m.handleList, read, get, nil)
	admin("/v1/admin/warehouses/detail", m.handleDetail, read, get, nil)
	admin("/v1/admin/warehouses/create", m.handleCreate, edit, post, middleware.DeserializeJsonOptional[createRequest]())
	admin("/v1/admin/warehouses/archive", m.warehouseAction(m.svc.Archive), approve, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/warehouses/restore", m.handleRestore, approve, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/warehouses/delete", m.warehouseAction(m.svc.Delete), approve, post, middleware.DeserializeJson[warehouseRequest]())

	admin("/v1/admin/revisions/detail", m.handleRevisionDetail, read, get, nil)
	admin("/v1/admin/revisions/save", m.handleSave, edit, post, middleware.DeserializeJson[SaveRequest]())
	admin("/v1/admin/revisions/open", m.handleOpen, edit, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/revisions/submit", m.handleSubmit, edit, post, middleware.DeserializeJson[revisionRequest]())
	// Withdraw: editor or approver (checked in the handler).
	admin("/v1/admin/revisions/withdraw", m.handleWithdraw, read, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/discard", m.handleDiscard, edit, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/approve", m.handleApprove, approve, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/reject", m.handleReject, approve, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/bulk-approve", m.handleBulkApprove, approve, post, middleware.DeserializeJson[bulkApproveRequest]())
	admin("/v1/admin/revisions/queue", m.handleQueue, read, get, nil)
	admin("/v1/admin/revisions/history", m.handleHistory, read, get, nil)

	admin("/v1/admin/geocode/preview", m.handleGeocodePreview, edit, post, middleware.DeserializeJson[geocodeRequest]())
	admin("/v1/admin/media", m.handleMediaList, read, get, nil)
	admin("/v1/admin/media/upload-url", m.handleUploadURL, edit, post, middleware.DeserializeJson[UploadRequest]())
	admin("/v1/admin/media/confirm", m.handleConfirm, edit, post, middleware.DeserializeJson[confirmRequest]())
	admin("/v1/admin/media/link", m.handleLink, read, get, nil)

	public := func(path string, h http.HandlerFunc) {
		middleware.Handle(path, h).WithMethods(get).With(appCtx.Middleware()).AllowCORS().WithLogEnabled()
	}
	public("/v1/public/listing", m.handlePublicListing)
	public("/v1/public/listing/slugs", m.handlePublicSlugs(false))
	public("/v1/public/sitemap", m.handlePublicSlugs(true))
}

// --- helpers ---

func body[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	v, ok := r.Context().Value(middleware.DeserializerContextKey).(T)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
	}
	return v, ok
}

func actorOf(r *http.Request) Actor {
	u := middleware.GetUserFromContext(r)
	if u == nil {
		return domain.SystemActor
	}
	return Actor{UserID: u.ID.Hex(), Email: strings.ToLower(u.Email)}
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

func (m *Module) listBounds() (int, int) {
	c := m.cfg()
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
		ve *validationError
		se *stateError
		sb *submitBlockedError
	)
	switch {
	case errors.As(err, &ve):
		badRequest(w, r, ve.msg)
	case errors.As(err, &sb):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: "fix these before submitting",
			ErrCode: "submit_blocked", Data: map[string]any{"problems": sb.Problems}})
	case errors.As(err, &se):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: se.msg, ErrCode: se.code})
	case errors.Is(err, errNotFound):
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

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	def, max := m.listBounds()
	pg, limit, ok := pageOf(r, def, max)
	if !ok {
		badRequest(w, r, "bad page or limit")
		return
	}
	q := r.URL.Query()
	f := WarehouseFilter{Status: q.Get("status"), Q: strings.TrimSpace(q.Get("q")), City: strings.TrimSpace(q.Get("city")), NeedsInfo: q.Get("needsInfo") == "true"}
	switch f.Status {
	case "", domain.WarehouseUnpublished, domain.WarehouseLive, domain.WarehouseArchived:
	default:
		badRequest(w, r, "status must be unpublished, live or archived")
		return
	}
	items, total, err := m.store.ListWarehouses(r.Context(), f, pg, limit)
	send(w, r, http.StatusOK, page[domain.Warehouse]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}

func (m *Module) handleDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	wh, err := m.store.GetWarehouse(ctx, id)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	media, err := m.store.ListMedia(ctx, id)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	history, err := m.store.Revisions(ctx, id)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	resp := detailResponse{Warehouse: wh, Media: media, History: history}
	if wh.OpenRevisionID != nil {
		if rev, err := m.store.GetRevision(ctx, *wh.OpenRevisionID); err == nil {
			resp.OpenRevision = rev
			resp.Preview = m.svc.preview(m.rules.Snapshot(), rev.Content, media)
		}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	req, _ := r.Context().Value(middleware.DeserializerContextKey).(createRequest)
	wh, res, err := m.svc.Create(r.Context(), actorOf(r), req.Content)
	send(w, r, http.StatusCreated, createResponse{Warehouse: wh, RevisionResult: res}, err)
}

func (m *Module) warehouseAction(fn func(ctx context.Context, a Actor, id primitive.ObjectID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := body[warehouseRequest](w, r)
		if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
			return
		}
		err := fn(r.Context(), actorOf(r), req.WarehouseID)
		send(w, r, http.StatusOK, map[string]any{"warehouseId": req.WarehouseID.Hex()}, err)
	}
}

func (m *Module) handleRestore(w http.ResponseWriter, r *http.Request) {
	req, ok := body[warehouseRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := m.svc.Restore(r.Context(), actorOf(r), req.WarehouseID)
	send(w, r, http.StatusCreated, res, err)
}

// --- revisions ---

func (m *Module) handleRevisionDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	rev, err := m.store.GetRevision(r.Context(), id)
	send(w, r, http.StatusOK, rev, err)
}

func (m *Module) handleSave(w http.ResponseWriter, r *http.Request) {
	req, ok := body[SaveRequest](w, r)
	if !ok || !requireID(w, r, req.RevisionID, "revisionId") {
		return
	}
	res, err := m.svc.Save(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, res, err)
}

func (m *Module) handleOpen(w http.ResponseWriter, r *http.Request) {
	req, ok := body[warehouseRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := m.svc.Open(r.Context(), actorOf(r), req.WarehouseID)
	send(w, r, http.StatusCreated, res, err)
}

func (m *Module) revisionAction(w http.ResponseWriter, r *http.Request, fn func(req revisionRequest) (RevisionResult, error)) {
	req, ok := body[revisionRequest](w, r)
	if !ok || !requireID(w, r, req.RevisionID, "revisionId") {
		return
	}
	res, err := fn(req)
	send(w, r, http.StatusOK, res, err)
}

func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	m.revisionAction(w, r, func(req revisionRequest) (RevisionResult, error) {
		return m.svc.Submit(r.Context(), actorOf(r), req.RevisionID, "")
	})
}

func (m *Module) handleWithdraw(w http.ResponseWriter, r *http.Request) {
	u := middleware.GetUserFromContext(r)
	if !authz.Has(u, authz.PermEditor) && !authz.Has(u, authz.PermApprover) {
		middleware.SendJSONError(w, r, apperrors.ErrAdminAccessRequired)
		return
	}
	m.revisionAction(w, r, func(req revisionRequest) (RevisionResult, error) {
		return m.svc.Withdraw(r.Context(), actorOf(r), req.RevisionID)
	})
}

func (m *Module) handleDiscard(w http.ResponseWriter, r *http.Request) {
	m.revisionAction(w, r, func(req revisionRequest) (RevisionResult, error) {
		return m.svc.Discard(r.Context(), actorOf(r), req.RevisionID)
	})
}

func (m *Module) handleApprove(w http.ResponseWriter, r *http.Request) {
	m.revisionAction(w, r, func(req revisionRequest) (RevisionResult, error) {
		return m.svc.Approve(r.Context(), actorOf(r), req.RevisionID, "")
	})
}

func (m *Module) handleReject(w http.ResponseWriter, r *http.Request) {
	m.revisionAction(w, r, func(req revisionRequest) (RevisionResult, error) {
		return m.svc.Reject(r.Context(), actorOf(r), req.RevisionID, req.Comment)
	})
}

func (m *Module) handleBulkApprove(w http.ResponseWriter, r *http.Request) {
	req, ok := body[bulkApproveRequest](w, r)
	if !ok {
		return
	}
	if len(req.RevisionIDs) == 0 {
		badRequest(w, r, "revisionIds is required")
		return
	}
	batchID, items, err := m.svc.BulkApprove(r.Context(), actorOf(r), req.RevisionIDs)
	send(w, r, http.StatusOK, map[string]any{"batchId": batchID, "items": items}, err)
}

func (m *Module) handleQueue(w http.ResponseWriter, r *http.Request) {
	def, max := m.listBounds()
	pg, limit, ok := pageOf(r, def, max)
	if !ok {
		badRequest(w, r, "bad page or limit")
		return
	}
	items, total, err := m.store.Queue(r.Context(), pg, limit)
	send(w, r, http.StatusOK, page[domain.Revision]{Items: items, Page: pg, Limit: limit, Total: total}, err)
}

func (m *Module) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "warehouseId")
	if !ok {
		return
	}
	items, err := m.store.Revisions(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"items": items}, err)
}

// --- geocode + media ---

func (m *Module) handleGeocodePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := body[geocodeRequest](w, r)
	if !ok {
		return
	}
	loc, err := m.svc.GeocodePreview(r.Context(), req.Address)
	send(w, r, http.StatusOK, loc, err)
}

func (m *Module) handleMediaList(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "warehouseId")
	if !ok {
		return
	}
	items, err := m.store.ListMedia(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"items": items}, err)
}

func (m *Module) handleUploadURL(w http.ResponseWriter, r *http.Request) {
	req, ok := body[UploadRequest](w, r)
	if !ok || !requireID(w, r, req.WarehouseID, "warehouseId") {
		return
	}
	res, err := m.media.UploadURL(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, res, err)
}

func (m *Module) handleConfirm(w http.ResponseWriter, r *http.Request) {
	req, ok := body[confirmRequest](w, r)
	if !ok || !requireID(w, r, req.MediaID, "mediaId") {
		return
	}
	md, err := m.media.Confirm(r.Context(), req.MediaID)
	send(w, r, http.StatusOK, md, err)
}

func (m *Module) handleLink(w http.ResponseWriter, r *http.Request) {
	id, ok := queryID(w, r, "id")
	if !ok {
		return
	}
	url, err := m.media.Link(r.Context(), id)
	send(w, r, http.StatusOK, map[string]any{"url": url}, err)
}

// --- public ---

func (m *Module) handlePublicListing(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	res, err := m.public.Lookup(r.Context(), slug)
	switch {
	case errors.Is(err, errNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case err != nil:
		sendErr(w, r, err)
	case res.RedirectTo != "":
		middleware.SendJSONResponse(w, r, http.StatusMovedPermanently, map[string]string{"redirectTo": res.RedirectTo})
	case res.IsGone:
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusGone, Message: "this listing has been removed",
			ErrCode: "gone", Data: map[string]any{"nearby": res.Gone}})
	default:
		middleware.SendJSONResponse(w, r, http.StatusOK, res.Listing)
	}
}

func (m *Module) handlePublicSlugs(withCover bool) http.HandlerFunc {
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
		items, err := m.public.Slugs(r.Context(), pg, withCover)
		send(w, r, http.StatusOK, map[string]any{"items": items, "page": pg}, err)
	}
}
