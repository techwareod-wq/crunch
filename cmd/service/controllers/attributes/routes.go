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
	middleware.Handle("/v1/admin/attributes/tree", http.HandlerFunc(HandleTree)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/nodes/create", http.HandlerFunc(HandleNodeCreate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[createNodeRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/nodes/update", http.HandlerFunc(HandleNodeUpdate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[attributeService.NodePatch]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/nodes/move", http.HandlerFunc(HandleNodeMove)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[moveRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/nodes/reorder", http.HandlerFunc(HandleNodeReorder)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[reorderNodesRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/fields/create", http.HandlerFunc(HandleFieldCreate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[fieldRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/fields/update", http.HandlerFunc(HandleFieldUpdate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[fieldRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/fields/reorder", http.HandlerFunc(HandleFieldReorder)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[reorderFieldsRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/nodes/delete", http.HandlerFunc(HandleNodeDelete)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[attributeService.DeleteTarget]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/fields/delete", http.HandlerFunc(HandleFieldDelete)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[attributeService.DeleteTarget]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/attributes/options/delete", http.HandlerFunc(HandleOptionDelete)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[attributeService.DeleteTarget]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/industries", http.HandlerFunc(HandleIndustries)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/industries/create", http.HandlerFunc(HandleIndustryCreate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[models.Industry]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/industries/update", http.HandlerFunc(HandleIndustryUpdate)).
		WithAdminAuthorization(authz.PermAttributes).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[attributeService.IndustryPatch]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/industries/delete", http.HandlerFunc(HandleIndustryDelete)).
		WithAdminAuthorization(authz.PermSuperuser).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[deleteIndustryRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
