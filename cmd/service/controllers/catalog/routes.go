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
// /v1/public (no auth, allowlisted DTOs).
func Handle(appCtx *config.AppContext) {
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

	admin("/v1/admin/warehouses", HandleList, read, get, nil)
	admin("/v1/admin/warehouses/detail", HandleDetail, read, get, nil)
	admin("/v1/admin/warehouses/create", HandleCreate, edit, post, middleware.DeserializeJsonOptional[createRequest]())
	admin("/v1/admin/warehouses/archive", HandleArchive, approve, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/warehouses/restore", HandleRestore, approve, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/warehouses/delete", HandleDelete, approve, post, middleware.DeserializeJson[warehouseRequest]())

	admin("/v1/admin/revisions/detail", HandleRevisionDetail, read, get, nil)
	admin("/v1/admin/revisions/save", HandleSave, edit, post, middleware.DeserializeJson[catalogService.SaveRequest]())
	admin("/v1/admin/revisions/open", HandleOpen, edit, post, middleware.DeserializeJson[warehouseRequest]())
	admin("/v1/admin/revisions/submit", HandleSubmit, edit, post, middleware.DeserializeJson[revisionRequest]())
	// Withdraw: editor or approver (checked in the handler).
	admin("/v1/admin/revisions/withdraw", HandleWithdraw, read, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/discard", HandleDiscard, edit, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/approve", HandleApprove, approve, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/reject", HandleReject, approve, post, middleware.DeserializeJson[revisionRequest]())
	admin("/v1/admin/revisions/bulk-approve", HandleBulkApprove, approve, post, middleware.DeserializeJson[bulkApproveRequest]())
	admin("/v1/admin/revisions/queue", HandleQueue, read, get, nil)
	admin("/v1/admin/revisions/history", HandleHistory, read, get, nil)

	// Needs-info queue (spec 02, D-039).
	admin("/v1/admin/needs-info/summary", HandleNeedsInfoSummary, read, get, nil)
	admin("/v1/admin/needs-info/list", HandleNeedsInfoList, read, get, nil)
	admin("/v1/admin/needs-info/answer", HandleNeedsInfoAnswer, edit, post, middleware.DeserializeJson[catalogService.AnswerRequest]())

	admin("/v1/admin/geocode/preview", HandleGeocodePreview, edit, post, middleware.DeserializeJson[geocodeRequest]())
	admin("/v1/admin/media", HandleMediaList, read, get, nil)
	admin("/v1/admin/media/upload-url", HandleUploadURL, edit, post, middleware.DeserializeJson[catalogService.UploadRequest]())
	admin("/v1/admin/media/confirm", HandleConfirm, edit, post, middleware.DeserializeJson[confirmRequest]())
	admin("/v1/admin/media/link", HandleLink, read, get, nil)

	public := func(path string, h http.HandlerFunc) {
		middleware.Handle(path, h).WithMethods(get).With(appCtx.Middleware()).AllowCORS().WithLogEnabled()
	}
	public("/v1/public/listing", HandlePublicListing)
	public("/v1/public/listing/slugs", HandlePublicSlugs(false))
	public("/v1/public/sitemap", HandlePublicSlugs(true))
}
