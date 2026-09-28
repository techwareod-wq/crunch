package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
)

// Handle registers the /v1/admin/* routes. Every route chains
// WithAdminAuthorization inside WithJWTAuthentication (last chained runs first,
// so JWT populates the user before the permission check) and NONE take
// WithAppSubscription: the gate checks the caller's subscription, which is
// irrelevant to ops work, and the target's subscription is often precisely
// what's broken. Admin-triggered work still shares the global tokentracker LLM
// budget because the same services and queues are reused.
//
// Every route is tagged with its domain permission (RBAC plan §3/§5) — the
// baseline admin.access is always required, plus each tagged permission. This
// is what makes future custom (e.g. support-tier) roles enforceable. Only
// whoami and the audit list are argless (baseline only). Shared multi-verb
// paths (roles, plans) are tagged with the READ permission and re-check the
// WRITE permission in-handler (the registry is path-only).
//
// Mirrors keep the same /delete, /edit suffix workarounds as the user routes —
// the registry is path-only, so verbs can't share a path.
func Handle(appCtx *config.AppContext) {
	// --- console (acting-admin identity + persisted audit trail) ---

	middleware.Handle("/v1/admin/whoami", http.HandlerFunc(HandleAdminWhoami)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/audit", http.HandlerFunc(HandleAdminListAuditActions)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- roles & permissions (RBAC) ---
	// GET lists (roles.read); POST upserts (roles.write, re-checked in-handler
	// since the path-only registry can't gate GET and POST differently).

	middleware.Handle("/v1/admin/roles", http.HandlerFunc(HandleAdminRoles)).
		WithAdminAuthorization(authz.PermRolesRead).
		WithJWTAuthentication().
		WithMethods("GET", "POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/roles/delete", http.HandlerFunc(HandleAdminDeleteRole)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteRoleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Catalog seed — the API twin of `rolesmigrate -seed-roles` (dry-run
	// default; the only creation path for the two company roles).
	middleware.Handle("/v1/admin/roles/seed", http.HandlerFunc(HandleAdminSeedRoles)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJsonOptional[adminSeedRolesRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- migrations (superuser-tier migrations.run) ---
	// API twin of cmd/companymigrate: migrate | verify | drop-user-index,
	// dry-run default. The CLI stays the recovery path.
	middleware.Handle("/v1/admin/company/migrate", http.HandlerFunc(HandleAdminCompanyMigrate)).
		WithAdminAuthorization(authz.PermMigrationsRun).
		WithJWTAuthentication().
		With(middleware.DeserializeJsonOptional[adminCompanyMigrateRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- users (dashboard entry points + profile + role/grants management) ---

	middleware.Handle("/v1/admin/users", http.HandlerFunc(HandleAdminListUsers)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/lookup", http.HandlerFunc(HandleAdminLookupUser)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/profile", http.HandlerFunc(HandleAdminGetProfile)).
		WithAdminAuthorization(authz.PermUsersRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/role", http.HandlerFunc(HandleAdminSetUserRole)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserRoleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/grants", http.HandlerFunc(HandleAdminSetUserGrants)).
		WithAdminAuthorization(authz.PermRolesWrite).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetUserGrantsRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/users/delete", http.HandlerFunc(HandleAdminDeleteUser)).
		WithAdminAuthorization(authz.PermUsersDelete).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteUserRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- onboarding ---

	middleware.Handle("/v1/admin/onboard", http.HandlerFunc(HandleAdminOnboard)).
		WithAdminAuthorization(authz.PermOnboardingManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminOnboardRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/web-entity", http.HandlerFunc(handleAdminWebEntity)).
		WithAdminAuthorization(authz.PermOnboardingManage).
		WithJWTAuthentication().
		WithMethods("GET", "PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/web-entity/delete", http.HandlerFunc(HandleAdminDeleteWebEntity)).
		WithAdminAuthorization(authz.PermUsersDelete).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDeleteWebEntityRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/onboarding-steps", http.HandlerFunc(HandleAdminGetOnboardingSteps)).
		WithAdminAuthorization(authz.PermOnboardingManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- site intelligence ---

	middleware.Handle("/v1/admin/site-intelligence/process", http.HandlerFunc(HandleAdminProcess)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminProcessRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/dispatch-funnel-classification", http.HandlerFunc(HandleAdminDispatchFunnelClassification)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDispatchFunnelClassificationRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/dispatch-opportunity-score", http.HandlerFunc(HandleAdminDispatchOpportunityScore)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDispatchOpportunityScoreRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/dispatch-clustering", http.HandlerFunc(HandleAdminDispatchClustering)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminDispatchClusteringRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/manual-keyword", http.HandlerFunc(HandleAdminAddManualKeyword)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminAddManualKeywordRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/keyword-data", http.HandlerFunc(HandleAdminGetKeywordData)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/cluster-supporting", http.HandlerFunc(HandleAdminGetClusterSupporting)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/site-intelligence/keyword-search", http.HandlerFunc(HandleAdminSearchKeywords)).
		WithAdminAuthorization(authz.PermSiteIntelManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- seo blog ---

	middleware.Handle("/v1/admin/seo-blog/orchestrate", http.HandlerFunc(HandleAdminOrchestrate)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminOrchestrateRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/seo-blog/retry", http.HandlerFunc(HandleAdminRetry)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminRetryRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- scheduled articles ---

	middleware.Handle("/v1/admin/scheduled-articles", http.HandlerFunc(HandleAdminGetScheduledArticles)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article", http.HandlerFunc(HandleAdminGetArticleBySchedule)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/schedule", http.HandlerFunc(HandleAdminScheduleArticle)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminScheduleArticleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/delete", http.HandlerFunc(HandleAdminDeleteScheduledArticle)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		WithMethods("DELETE").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/draft", http.HandlerFunc(HandleAdminSaveArticleDraft)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSaveDraftRequest]()).
		WithMethods("PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/image-upload-url", http.HandlerFunc(HandleAdminCreateImageUploadURL)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminImageUploadURLRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/inline-image-upload-url", http.HandlerFunc(HandleAdminCreateInlineImageUploadURL)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminInlineImageUploadURLRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/suggest-titles", http.HandlerFunc(HandleAdminSuggestTitles)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSuggestTitlesRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/retitle-preview", http.HandlerFunc(HandleAdminRetitlePreview)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminRetitlePreviewRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/edit", http.HandlerFunc(HandleAdminUpdateScheduledArticle)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminUpdateScheduledArticleRequest]()).
		WithMethods("PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/scheduled-articles/article/publish-state", http.HandlerFunc(HandleAdminSetPublishState)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSetPublishStateRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- scheduling engine ---

	middleware.Handle("/v1/admin/scheduling/rerun", http.HandlerFunc(HandleAdminRerunSchedule)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminRerunScheduleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- payments (read + support-request-only writes; no checkout mirror) ---

	middleware.Handle("/v1/admin/payments/status", http.HandlerFunc(HandleAdminGetPaymentStatus)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/payments/plans", http.HandlerFunc(HandleAdminGetPlans)).
		WithAdminAuthorization(authz.PermPlansRead).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/payments/transactions", http.HandlerFunc(HandleAdminListTransactions)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/payments/cancel", http.HandlerFunc(HandleAdminCancelSubscription)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminCancelSubscriptionRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/payments/resubscribe", http.HandlerFunc(HandleAdminResubscribe)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminResubscribeRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- entitlements (trials, overrides, comps, recompute, plan catalog) ---
	// Paths deliberately avoid /v1/admin/payments/* — /v1/admin/payments/plans
	// already serves the Paddle price listing and the registry panics on
	// duplicate paths.

	middleware.Handle("/v1/admin/trials", http.HandlerFunc(HandleAdminListTrials)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/trials/extend", http.HandlerFunc(HandleAdminExtendTrial)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminExtendTrialRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/trials/end", http.HandlerFunc(HandleAdminEndTrial)).
		WithAdminAuthorization(authz.PermTrialsManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminEndTrialRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/entitlements", http.HandlerFunc(HandleAdminGetEntitlements)).
		WithAdminAuthorization(authz.PermEntitlementsGrant).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/entitlements/overrides", http.HandlerFunc(HandleAdminPutOverrides)).
		WithAdminAuthorization(authz.PermEntitlementsGrant).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminPutOverridesRequest]()).
		WithMethods("PUT").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Comp grant/revoke (PUT/DELETE on one path — manual decode in-handler,
	// mirroring /plans' multi-verb style).
	middleware.Handle("/v1/admin/entitlements/comp", http.HandlerFunc(handleAdminComp)).
		WithAdminAuthorization(authz.PermEntitlementsGrant).
		WithJWTAuthentication().
		WithMethods("PUT", "DELETE").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/entitlements/recompute", http.HandlerFunc(HandleAdminRecompute)).
		WithAdminAuthorization(authz.PermEntitlementsGrant).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminRecomputeRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Plan catalog CRUD. GET lists (plans.read); POST/PUT upsert billing/pricing
	// (plans.write, re-checked in-handler — the path-only registry can't gate
	// GET and the write verbs differently).
	middleware.Handle("/v1/admin/plans", http.HandlerFunc(handleAdminPlans)).
		WithAdminAuthorization(authz.PermPlansRead).
		WithJWTAuthentication().
		WithMethods("GET", "POST", "PUT").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- cron (force-run + job listing) ---

	middleware.Handle("/v1/admin/cron/run", http.HandlerFunc(HandleAdminCronRun)).
		WithAdminAuthorization(authz.PermCronManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminCronRunRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/cron/jobs", http.HandlerFunc(HandleAdminCronJobs)).
		WithAdminAuthorization(authz.PermCronManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- analytics (replay + connection ops view) ---
	// Replay rewrites facts history (delete-then-rebuild) — superuser
	// migrations permission, like the panel's data migrations.

	middleware.Handle("/v1/admin/analytics/replay", http.HandlerFunc(HandleAdminAnalyticsReplay)).
		WithAdminAuthorization(authz.PermMigrationsRun).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminAnalyticsReplayRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/replay/status", http.HandlerFunc(HandleAdminAnalyticsReplayStatus)).
		WithAdminAuthorization(authz.PermMigrationsRun).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Backfill fetches deep history through the shared ingest process —
	// additive and idempotent (no delete), but a heavy quota-spending sweep,
	// so it rides the same superuser permission as replay.
	middleware.Handle("/v1/admin/analytics/backfill", http.HandlerFunc(HandleAdminAnalyticsBackfill)).
		WithAdminAuthorization(authz.PermMigrationsRun).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminAnalyticsBackfillRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/analytics/connections", http.HandlerFunc(HandleAdminAnalyticsConnections)).
		WithAdminAuthorization().
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- seo audit (per-user run visibility + resume-from-failed-stage retry) ---
	// Namespaced /v1/admin/seo-audit/* — /v1/admin/audit is the admin ACTION
	// log and the registry panics on duplicate paths. Deliberately NOT behind
	// WithAuditFeature: ops can inspect and retry while the user-facing
	// feature is dark.

	middleware.Handle("/v1/admin/seo-audit/runs", http.HandlerFunc(HandleAdminSeoAuditRuns)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/seo-audit/run", http.HandlerFunc(HandleAdminSeoAuditRun)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/admin/seo-audit/retry", http.HandlerFunc(HandleAdminSeoAuditRetry)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminSeoAuditRetryRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// --- content bridge ---

	middleware.Handle("/v1/admin/content-bridge/publish", http.HandlerFunc(HandleAdminPublishArticle)).
		WithAdminAuthorization(authz.PermContentManage).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[adminPublishRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
