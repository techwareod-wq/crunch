package catalog

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
)

// Handle registers the catalog routes (spec 03): admin listing lifecycle,
// the Needs-info queue, media and geocoding under /v1/admin, and the public listing API under
// /v1/public (sign-in plus the listings feature, allowlisted DTOs).
func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/admin/warehouses", http.HandlerFunc(HandleList)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/warehouses/detail", http.HandlerFunc(HandleDetail)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/warehouses/create", http.HandlerFunc(HandleCreate)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJsonOptional[createRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/warehouses/archive", http.HandlerFunc(HandleArchive)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[warehouseRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/warehouses/restore", http.HandlerFunc(HandleRestore)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[warehouseRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/warehouses/delete", http.HandlerFunc(HandleDelete)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[warehouseRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/detail", http.HandlerFunc(HandleRevisionDetail)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/save", http.HandlerFunc(HandleSave)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[catalogService.SaveRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/open", http.HandlerFunc(HandleOpen)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[warehouseRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/submit", http.HandlerFunc(HandleSubmit)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[revisionRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Withdraw: editor or approver (checked in the handler).
	middleware.Handle("/v1/admin/revisions/withdraw", http.HandlerFunc(HandleWithdraw)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		With(middleware.DeserializeJson[revisionRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/discard", http.HandlerFunc(HandleDiscard)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[revisionRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/approve", http.HandlerFunc(HandleApprove)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[revisionRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/reject", http.HandlerFunc(HandleReject)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[revisionRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/bulk-approve", http.HandlerFunc(HandleBulkApprove)).
		WithAdminAuthorization(authz.PermApprover).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[bulkApproveRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/queue", http.HandlerFunc(HandleQueue)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/revisions/history", http.HandlerFunc(HandleHistory)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Needs-info queue (spec 02, D-039).
	middleware.Handle("/v1/admin/needs-info/summary", http.HandlerFunc(HandleNeedsInfoSummary)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/needs-info/list", http.HandlerFunc(HandleNeedsInfoList)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/needs-info/answer", http.HandlerFunc(HandleNeedsInfoAnswer)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[catalogService.AnswerRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/geocode/preview", http.HandlerFunc(HandleGeocodePreview)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[geocodeRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/media", http.HandlerFunc(HandleMediaList)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/media/upload-url", http.HandlerFunc(HandleUploadURL)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[catalogService.UploadRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/media/confirm", http.HandlerFunc(HandleConfirm)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[confirmRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/media/link", http.HandlerFunc(HandleLink)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/listing", http.HandlerFunc(HandlePublicListing)).
		WithFeature(authz.FeatureListings).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/listing/slugs", HandlePublicSlugs(false)).
		WithFeature(authz.FeatureListings).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/public/sitemap", HandlePublicSlugs(true)).
		WithFeature(authz.FeatureListings).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
