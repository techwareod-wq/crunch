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
// (a few dozen docs) so an admin never edits against another instance's
// stale cache.

type createNodeRequest struct {
	Node    domain.Node    `json:"node"`
	Default NewNodeDefault `json:"default"`
}

type moveRequest struct {
	Key             string `json:"key"`
	ExpectedVersion int    `json:"expectedVersion"`
	NewParentKey    string `json:"newParentKey"`
	Order           int    `json:"order"`
}

type reorderNodesRequest struct {
	ParentKey string   `json:"parentKey"`
	Keys      []string `json:"keys"`
}

type fieldRequest struct {
	Node            string       `json:"node"`
	ExpectedVersion int          `json:"expectedVersion"`
	Field           domain.Field `json:"field"`
}

type reorderFieldsRequest struct {
	Node            string   `json:"node"`
	ExpectedVersion int      `json:"expectedVersion"`
	Keys            []string `json:"keys"`
}

type deleteIndustryRequest struct {
	Key             string `json:"key"`
	ExpectedVersion int    `json:"expectedVersion"`
}

type treeResponse struct {
	RulesVersion int64             `json:"rulesVersion"`
	Tree         []domain.TreeNode `json:"tree"`
	// Validations lists the registered validation kinds and the field types
	// each applies to (for the admin field form).
	Validations map[string][]domain.FieldType `json:"validations"`
}

type industriesResponse struct {
	RulesVersion int64             `json:"rulesVersion"`
	Items        []domain.Industry `json:"items"`
}

type nodeResponse struct {
	Node domain.Node `json:"node"`
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
	route("/v1/admin/attributes/nodes/create", m.handleNodeCreate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[createNodeRequest]())
	route("/v1/admin/attributes/nodes/update", m.handleNodeUpdate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[NodePatch]())
	route("/v1/admin/attributes/nodes/move", m.handleNodeMove, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[moveRequest]())
	route("/v1/admin/attributes/nodes/reorder", m.handleNodeReorder, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[reorderNodesRequest]())
	route("/v1/admin/attributes/fields/create", m.handleFieldCreate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[fieldRequest]())
	route("/v1/admin/attributes/fields/update", m.handleFieldUpdate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[fieldRequest]())
	route("/v1/admin/attributes/fields/reorder", m.handleFieldReorder, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[reorderFieldsRequest]())

	route("/v1/admin/industries", m.handleIndustries, authz.PermAdmin, http.MethodGet, nil)
	route("/v1/admin/industries/create", m.handleIndustryCreate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[domain.Industry]())
	route("/v1/admin/industries/update", m.handleIndustryUpdate, authz.PermAttributes, http.MethodPost, middleware.DeserializeJson[IndustryPatch]())
	route("/v1/admin/industries/delete", m.handleIndustryDelete, authz.PermSuperuser, http.MethodPost, middleware.DeserializeJson[deleteIndustryRequest]())
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
	var ve *validationError
	switch {
	case errors.As(err, &ve):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: ve.msg, ErrCode: "invalid_rule"})
	case errors.Is(err, errNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case errors.Is(err, errKeyExists):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "key already exists", ErrCode: "key_exists"})
	case errors.Is(err, errVersionConflict):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "changed since last read — reload and retry", ErrCode: "version_conflict"})
	default:
		middleware.GetLogger(r).Error("attributes write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	}
}

// sendNode writes a node result (201 on create).
func sendNode(w http.ResponseWriter, r *http.Request, status int, n domain.Node, res WriteResult, err error) {
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, status, nodeResponse{Node: n, WriteResult: res})
}

func (m *Module) handleTree(w http.ResponseWriter, r *http.Request) {
	snap, err := m.store.Load(r.Context())
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, treeResponse{RulesVersion: snap.Version, Tree: snap.Tree(), Validations: domain.ValidationKinds()})
}

func (m *Module) handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[createNodeRequest](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.CreateNode(r.Context(), actorOf(r), req.Node, req.Default)
	sendNode(w, r, http.StatusCreated, n, res, err)
}

func (m *Module) handleNodeUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[NodePatch](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.UpdateNode(r.Context(), actorOf(r), req)
	sendNode(w, r, http.StatusOK, n, res, err)
}

func (m *Module) handleNodeMove(w http.ResponseWriter, r *http.Request) {
	req, ok := body[moveRequest](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.MoveNode(r.Context(), actorOf(r), req.Key, req.ExpectedVersion, req.NewParentKey, req.Order)
	sendNode(w, r, http.StatusOK, n, res, err)
}

func (m *Module) handleNodeReorder(w http.ResponseWriter, r *http.Request) {
	req, ok := body[reorderNodesRequest](w, r)
	if !ok {
		return
	}
	res, err := m.svc.ReorderNodes(r.Context(), actorOf(r), req.ParentKey, req.Keys)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, res)
}

func (m *Module) handleFieldCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[fieldRequest](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.CreateField(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Field)
	sendNode(w, r, http.StatusCreated, n, res, err)
}

func (m *Module) handleFieldUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[fieldRequest](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.UpdateField(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Field)
	sendNode(w, r, http.StatusOK, n, res, err)
}

func (m *Module) handleFieldReorder(w http.ResponseWriter, r *http.Request) {
	req, ok := body[reorderFieldsRequest](w, r)
	if !ok {
		return
	}
	n, res, err := m.svc.ReorderFields(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Keys)
	sendNode(w, r, http.StatusOK, n, res, err)
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

func (m *Module) handleIndustryDelete(w http.ResponseWriter, r *http.Request) {
	req, ok := body[deleteIndustryRequest](w, r)
	if !ok {
		return
	}
	res, err := m.svc.DeleteIndustry(r.Context(), actorOf(r), req.Key, req.ExpectedVersion)
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, res)
}
