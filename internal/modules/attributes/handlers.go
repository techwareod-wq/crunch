package attributes

import (
	"errors"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Admin routes (spec 02 "Admin endpoints"). Reads load straight from Mongo
// (≈60 docs) so an admin never edits against another instance's stale cache.

type moveRequest struct {
	Key          string `json:"key"`
	NewParentKey string `json:"newParentKey"`
	Order        int    `json:"order"`
}

type reorderRequest struct {
	ParentKey string   `json:"parentKey"`
	Keys      []string `json:"keys"`
}

type keyRequest struct {
	Key     string `json:"key"`
	Confirm bool   `json:"confirm"`
}

type seedRequest struct {
	Apply bool `json:"apply"`
}

type treeResponse struct {
	RulesVersion int64             `json:"rulesVersion"`
	Tree         []domain.TreeNode `json:"tree"`
}

type industriesResponse struct {
	RulesVersion int64             `json:"rulesVersion"`
	Items        []domain.Industry `json:"items"`
}

type defResponse struct {
	Def domain.AttrDef `json:"def"`
	WriteResult
}

type industryResponse struct {
	Industry domain.Industry `json:"industry"`
	WriteResult
}

func (m *Module) RegisterRoutes(appCtx *config.AppContext) {
	route := func(path string, h http.HandlerFunc, perm authz.Permission, method string, body func(http.Handler) http.Handler) {
		var required []authz.Permission
		if perm != authz.PermAdmin {
			required = append(required, perm)
		}
		p := middleware.Handle(path, h).
			WithAdminAuthorization(required...).
			WithJWTAuthentication()
		if body != nil {
			p = p.With(body)
		}
		p.WithMethods(method).
			With(appCtx.Middleware()).
			AllowCORS().
			WithLogEnabled()
	}
	route("/v1/admin/attributes/tree", m.handleTree, authz.PermAdmin, http.MethodGet, nil)
	route("/v1/admin/attributes/create", m.handleCreate, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[domain.AttrDef]())
	route("/v1/admin/attributes/update", m.handleUpdate, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[DefPatch]())
	route("/v1/admin/attributes/move", m.handleMove, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[moveRequest]())
	route("/v1/admin/attributes/reorder", m.handleReorder, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[reorderRequest]())
	route("/v1/admin/attributes/retire", m.handleRetire, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[keyRequest]())
	route("/v1/admin/attributes/restore", m.handleRestore, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[keyRequest]())
	// Seed is superuser-only; dry run unless {apply:true}.
	route("/v1/admin/attributes/seed", m.handleSeed, authz.PermSuperuser, http.MethodPost, middleware.DeserializeJsonOptional[seedRequest]())

	route("/v1/admin/industries", m.handleIndustries, authz.PermAdmin, http.MethodGet, nil)
	route("/v1/admin/industries/create", m.handleIndustryCreate, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[domain.Industry]())
	route("/v1/admin/industries/update", m.handleIndustryUpdate, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[IndustryPatch]())
	route("/v1/admin/industries/retire", m.handleIndustryRetire, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[keyRequest]())
	route("/v1/admin/industries/restore", m.handleIndustryRestore, authz.PermApprover, http.MethodPost, middleware.DeserializeJson[keyRequest]())
}

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

// sendErr maps service errors to HTTP.
func sendErr(w http.ResponseWriter, r *http.Request, err error) {
	var (
		ve *validationError
		be *retireBlockedError
		ce *confirmRequiredError
	)
	switch {
	case errors.As(err, &ve):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: ve.msg, ErrCode: "invalid_rule"})
	case errors.Is(err, errNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case errors.Is(err, errKeyExists):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "key already exists", ErrCode: "key_exists"})
	case errors.Is(err, errVersionConflict):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "changed since last read — reload and retry", ErrCode: "version_conflict"})
	case errors.As(err, &be):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "still referenced by rules — edit those first (D-042)", ErrCode: "retire_blocked", Data: be})
	case errors.As(err, &ce):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "this also retires its descendants — resend with confirm:true", ErrCode: "confirm_required", Data: ce})
	default:
		middleware.GetLogger(r).Error("attributes write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
}

func (m *Module) handleTree(w http.ResponseWriter, r *http.Request) {
	snap, err := m.store.Load(r.Context())
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, treeResponse{RulesVersion: snap.Version, Tree: snap.Tree()})
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[domain.AttrDef](w, r)
	if !ok {
		return
	}
	d, res, err := m.svc.CreateDef(r.Context(), actorOf(r), req)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusCreated, defResponse{Def: d, WriteResult: res})
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[DefPatch](w, r)
	if !ok {
		return
	}
	d, res, err := m.svc.UpdateDef(r.Context(), actorOf(r), req)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, defResponse{Def: d, WriteResult: res})
}

func (m *Module) handleMove(w http.ResponseWriter, r *http.Request) {
	req, ok := body[moveRequest](w, r)
	if !ok {
		return
	}
	d, res, err := m.svc.MoveDef(r.Context(), actorOf(r), req.Key, req.NewParentKey, req.Order)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, defResponse{Def: d, WriteResult: res})
}

func (m *Module) handleReorder(w http.ResponseWriter, r *http.Request) {
	req, ok := body[reorderRequest](w, r)
	if !ok {
		return
	}
	res, err := m.svc.Reorder(r.Context(), actorOf(r), req.ParentKey, req.Keys)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, res)
}

func (m *Module) handleRetire(w http.ResponseWriter, r *http.Request) {
	req, ok := body[keyRequest](w, r)
	if !ok {
		return
	}
	res, err := m.svc.RetireDef(r.Context(), actorOf(r), req.Key, req.Confirm)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, res)
}

func (m *Module) handleRestore(w http.ResponseWriter, r *http.Request) {
	req, ok := body[keyRequest](w, r)
	if !ok {
		return
	}
	d, res, err := m.svc.RestoreDef(r.Context(), actorOf(r), req.Key)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, defResponse{Def: d, WriteResult: res})
}

func (m *Module) handleSeed(w http.ResponseWriter, r *http.Request) {
	req, _ := r.Context().Value(middleware.DeserializerContextKey).(seedRequest)
	rep, err := m.svc.Seed(r.Context(), actorOf(r), req.Apply)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, rep)
}

func (m *Module) handleIndustries(w http.ResponseWriter, r *http.Request) {
	snap, err := m.store.Load(r.Context())
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, industriesResponse{RulesVersion: snap.Version, Items: snap.Industries})
}

func (m *Module) handleIndustryCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[domain.Industry](w, r)
	if !ok {
		return
	}
	ind, res, err := m.svc.CreateIndustry(r.Context(), actorOf(r), req)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusCreated, industryResponse{Industry: ind, WriteResult: res})
}

func (m *Module) handleIndustryUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[IndustryPatch](w, r)
	if !ok {
		return
	}
	ind, res, err := m.svc.UpdateIndustry(r.Context(), actorOf(r), req)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, industryResponse{Industry: ind, WriteResult: res})
}

func (m *Module) handleIndustryRetire(w http.ResponseWriter, r *http.Request) {
	m.setIndustryRetired(w, r, true)
}

func (m *Module) handleIndustryRestore(w http.ResponseWriter, r *http.Request) {
	m.setIndustryRetired(w, r, false)
}

func (m *Module) setIndustryRetired(w http.ResponseWriter, r *http.Request, retired bool) {
	req, ok := body[keyRequest](w, r)
	if !ok {
		return
	}
	ind, res, err := m.svc.SetIndustryRetired(r.Context(), actorOf(r), req.Key, retired)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, industryResponse{Industry: ind, WriteResult: res})
}
