package attributes

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
)

// Handle registers the attribute tree + industry routes (spec 02 "Admin
// endpoints"): reads for any admin, tree/industry writes for the
// `attributes` permission, hard deletes (nodes/fields/options, industries)
// for superusers (D-122, D-129).
func Handle(appCtx *config.AppContext) {
	route := func(path string, h http.HandlerFunc, method string, body func(http.Handler) http.Handler, required ...authz.Permission) {
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
	get, post := http.MethodGet, http.MethodPost
	attrs := authz.PermAttributes

	route("/v1/admin/attributes/tree", HandleTree, get, nil)
	route("/v1/admin/attributes/nodes/create", HandleNodeCreate, post, middleware.DeserializeJson[createNodeRequest](), attrs)
	route("/v1/admin/attributes/nodes/update", HandleNodeUpdate, post, middleware.DeserializeJson[attributeService.NodePatch](), attrs)
	route("/v1/admin/attributes/nodes/move", HandleNodeMove, post, middleware.DeserializeJson[moveRequest](), attrs)
	route("/v1/admin/attributes/nodes/reorder", HandleNodeReorder, post, middleware.DeserializeJson[reorderNodesRequest](), attrs)
	route("/v1/admin/attributes/fields/create", HandleFieldCreate, post, middleware.DeserializeJson[fieldRequest](), attrs)
	route("/v1/admin/attributes/fields/update", HandleFieldUpdate, post, middleware.DeserializeJson[fieldRequest](), attrs)
	route("/v1/admin/attributes/fields/reorder", HandleFieldReorder, post, middleware.DeserializeJson[reorderFieldsRequest](), attrs)
	route("/v1/admin/attributes/nodes/delete", HandleNodeDelete, post, middleware.DeserializeJson[attributeService.DeleteTarget](), authz.PermSuperuser)
	route("/v1/admin/attributes/fields/delete", HandleFieldDelete, post, middleware.DeserializeJson[attributeService.DeleteTarget](), authz.PermSuperuser)
	route("/v1/admin/attributes/options/delete", HandleOptionDelete, post, middleware.DeserializeJson[attributeService.DeleteTarget](), authz.PermSuperuser)

	route("/v1/admin/industries", HandleIndustries, get, nil)
	route("/v1/admin/industries/create", HandleIndustryCreate, post, middleware.DeserializeJson[models.Industry](), attrs)
	route("/v1/admin/industries/update", HandleIndustryUpdate, post, middleware.DeserializeJson[attributeService.IndustryPatch](), attrs)
	route("/v1/admin/industries/delete", HandleIndustryDelete, post, middleware.DeserializeJson[deleteIndustryRequest](), authz.PermSuperuser)
}
