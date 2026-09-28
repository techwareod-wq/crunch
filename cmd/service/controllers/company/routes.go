// Package company exposes the dashboard company-tenancy API (tenancy plan
// P4): the active-company summary, the switcher, member/invite/role
// management, the D19 invite-accept endpoint, and ownership transfer. Joining
// a company happens ONLY through the emailed invite-accept link. Company
// authorization goes through the active-company middleware +
// EffectiveCompanyPermissions ONLY — never the global RBAC resolver (D17).
//
// The whole surface sits behind the company.enabled values switch: every
// route chains WithCompanyFeature, so with the flag off each answers a JSON
// 404 masquerade (CORS intact) and does zero auth work. Routes are always
// registered — only the gate's answer changes.
package company

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/providers/impl/mailer"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// companyMailer sends the invite mails. Package seam so tests can fake it;
// wired from config in Handle.
var companyMailer interfaces.Mailer

func Handle(appCtx *config.AppContext) {
	companyMailer = mailer.New(appCtx.Config.Mailer)

	// Active-company routes: JWT resolves the user, WithActiveCompany resolves
	// (company, membership, perms) — chained before JWT so JWT runs first.
	middleware.Handle("/v1/company", http.HandlerFunc(HandleCompany)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodGet, http.MethodPatch).
		WithLogEnabled()

	middleware.Handle("/v1/company/members", http.HandlerFunc(HandleListMembers)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/company/members/invite", http.HandlerFunc(HandleInviteMember)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/members/revoke", http.HandlerFunc(HandleRevokeMember)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/members/role", http.HandlerFunc(HandleSetMemberRole)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/transfer", http.HandlerFunc(HandleTransferOwnership)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	// Company (team seat) billing — all PermCompanyBillingManage-gated;
	// checkout/cancel/resume additionally owner-verified in-service (D12).
	// Dark until PADDLE_TEAM_PRODUCT_ID is set: checkout 400s cleanly, the
	// summary answers zeros, cancel/resume 404 with no linked subscription.
	middleware.Handle("/v1/company/billing", http.HandlerFunc(HandleGetCompanyBilling)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	middleware.Handle("/v1/company/billing/checkout", http.HandlerFunc(HandleCompanyCheckout)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/billing/cancel", http.HandlerFunc(HandleCompanyCancel)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/billing/resume", http.HandlerFunc(HandleCompanyResume)).
		WithActiveCompany().
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	// JWT-only routes: these resolve their own scope (the caller's own
	// memberships / a token), no active-company context needed.
	middleware.Handle("/v1/company/mine", http.HandlerFunc(HandleMyCompanies)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	// Team-company creation (purchase flow step 1) — the caller becomes the
	// owner, so no active-company context applies yet.
	middleware.Handle("/v1/company/create", http.HandlerFunc(HandleCreateCompany)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/switch", http.HandlerFunc(HandleSwitchCompany)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()

	middleware.Handle("/v1/company/invites", http.HandlerFunc(HandlePendingInvites)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodGet).
		WithLogEnabled()

	// The D19 accept endpoint: valid unexpired token + authenticated Clerk
	// session + verified email match — all three, or no claim.
	middleware.Handle("/v1/company/invites/accept", http.HandlerFunc(HandleAcceptInvite)).
		WithJWTAuthentication().
		With(appCtx.Middleware()).
		WithCompanyFeature().
		AllowCORS().
		WithMethods(http.MethodPost).
		WithLogEnabled()
}
