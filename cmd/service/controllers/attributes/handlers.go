package attributes

import (
	"errors"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type createNodeRequest struct {
	Node    models.AttributeNode            `json:"node"`
	Default attributeService.NewNodeDefault `json:"default"`
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
	Node            string                `json:"node"`
	ExpectedVersion int                   `json:"expectedVersion"`
	Field           models.AttributeField `json:"field"`
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
	Validations map[string][]models.FieldType `json:"validations"`
}

type industriesResponse struct {
	RulesVersion int64             `json:"rulesVersion"`
	Items        []models.Industry `json:"items"`
}

type nodeResponse struct {
	Node models.AttributeNode `json:"node"`
	attributeService.WriteResult
}

type industryResponse struct {
	Industry models.Industry `json:"industry"`
	attributeService.WriteResult
}

func service(r *http.Request) attributeService.AttributeService {
	return config.GetAppContext(r).InternalServices.AttributeService
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
	var ve *attributeService.ValidationError
	switch {
	case errors.As(err, &ve):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: ve.Msg, ErrCode: "invalid_rule"})
	case errors.Is(err, attributeService.ErrNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
	case errors.Is(err, attributeService.ErrKeyExists):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "key already exists", ErrCode: "key_exists"})
	case errors.Is(err, attributeService.ErrVersionConflict):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusConflict, Message: "changed since last read — reload and retry", ErrCode: "version_conflict"})
	default:
		middleware.GetLogger(r).Error("attributes write failed", "error", err)
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

func HandleTree(w http.ResponseWriter, r *http.Request) {
	snap, err := service(r).Snapshot(r.Context())
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, treeResponse{RulesVersion: snap.Version, Tree: snap.Tree(), Validations: domain.ValidationKinds()})
}

func HandleNodeCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[createNodeRequest](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).CreateNode(r.Context(), actorOf(r), req.Node, req.Default)
	send(w, r, http.StatusCreated, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleNodeUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[attributeService.NodePatch](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).UpdateNode(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleNodeMove(w http.ResponseWriter, r *http.Request) {
	req, ok := body[moveRequest](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).MoveNode(r.Context(), actorOf(r), req.Key, req.ExpectedVersion, req.NewParentKey, req.Order)
	send(w, r, http.StatusOK, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleNodeReorder(w http.ResponseWriter, r *http.Request) {
	req, ok := body[reorderNodesRequest](w, r)
	if !ok {
		return
	}
	res, err := service(r).ReorderNodes(r.Context(), actorOf(r), req.ParentKey, req.Keys)
	send(w, r, http.StatusOK, res, err)
}

func HandleFieldCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[fieldRequest](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).CreateField(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Field)
	send(w, r, http.StatusCreated, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleFieldUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[fieldRequest](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).UpdateField(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Field)
	send(w, r, http.StatusOK, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleFieldReorder(w http.ResponseWriter, r *http.Request) {
	req, ok := body[reorderFieldsRequest](w, r)
	if !ok {
		return
	}
	n, res, err := service(r).ReorderFields(r.Context(), actorOf(r), req.Node, req.ExpectedVersion, req.Keys)
	send(w, r, http.StatusOK, nodeResponse{Node: n, WriteResult: res}, err)
}

func HandleIndustries(w http.ResponseWriter, r *http.Request) {
	snap, err := service(r).Snapshot(r.Context())
	if err != nil {
		sendErr(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, industriesResponse{RulesVersion: snap.Version, Items: snap.Industries})
}

func HandleIndustryCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[models.Industry](w, r)
	if !ok {
		return
	}
	ind, res, err := service(r).CreateIndustry(r.Context(), actorOf(r), req)
	send(w, r, http.StatusCreated, industryResponse{Industry: ind, WriteResult: res}, err)
}

func HandleIndustryUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := body[attributeService.IndustryPatch](w, r)
	if !ok {
		return
	}
	ind, res, err := service(r).UpdateIndustry(r.Context(), actorOf(r), req)
	send(w, r, http.StatusOK, industryResponse{Industry: ind, WriteResult: res}, err)
}

func HandleIndustryDelete(w http.ResponseWriter, r *http.Request) {
	req, ok := body[deleteIndustryRequest](w, r)
	if !ok {
		return
	}
	res, err := service(r).DeleteIndustry(r.Context(), actorOf(r), req.Key, req.ExpectedVersion)
	send(w, r, http.StatusOK, res, err)
}
