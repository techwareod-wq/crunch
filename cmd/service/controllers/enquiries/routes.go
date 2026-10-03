package enquiries

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
)

// Handle registers the enquiry routes (spec 06): the signed-in visitor's
// form, and the inbox for the editor permission (D-112).
func Handle(appCtx *config.AppContext) {
	// D-018: no captcha and no rate limit; Clerk sign-in plus the enquiries
	// feature are the only gates.
	middleware.Handle("/v1/enquiries", http.HandlerFunc(HandleSubmit)).
		WithFeature(authz.FeatureEnquiries).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[enquiryService.SubmitRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries", http.HandlerFunc(HandleList)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries/detail", http.HandlerFunc(HandleDetail)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries/status", http.HandlerFunc(HandleStatus)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[enquiryService.StatusRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries/assign", http.HandlerFunc(HandleAssign)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[enquiryService.AssignRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries/notes", http.HandlerFunc(HandleNote)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[enquiryService.NoteRequest]()).
		WithMethods(http.MethodPost).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/enquiries/export", http.HandlerFunc(HandleExport)).
		WithAdminAuthorization(authz.PermEditor).
		WithJWTAuthentication().
		WithMethods(http.MethodGet).
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
