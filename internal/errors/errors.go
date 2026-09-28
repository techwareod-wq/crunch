package apperrors

import "net/http"

// AppError pairs an HTTP status code with a user-facing message.
type Error struct {
	Code    int
	Message string
	// ErrCode is a machine-readable discriminator clients branch on
	// (e.g. "subscription_required" vs "feature_not_included" — both 402).
	// Empty = omitted from the wire, keeping legacy envelopes byte-identical.
	ErrCode string
	// Data carries structured context for the client (nil = omitted).
	Data any
}

func (e *Error) Error() string {
	return e.Message
}

func newError(code int, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// FeatureNotIncluded (402, code feature_not_included) — the caller has a
// valid subscription for the app but their plan doesn't include the feature.
// The frontend uses data.{app, feature} to render an upgrade prompt instead
// of the paywall.
func FeatureNotIncluded(app, feature string) *Error {
	return &Error{
		Code:    http.StatusPaymentRequired,
		Message: "feature not included in your plan",
		ErrCode: "feature_not_included",
		Data:    map[string]string{"app": app, "feature": feature},
	}
}

// TrialLimitReached (402, code trial_limit_reached) — a trial user hit a
// numeric trial cap (currently: article generations started). The frontend
// uses data.{limit, used} to render the upgrade CTA with progress, distinct
// from feature_not_included (feature absent from plan) and
// subscription_required (no access at all).
func TrialLimitReached(limit, used int) *Error {
	return &Error{
		Code:    http.StatusPaymentRequired,
		Message: "trial limit reached",
		ErrCode: "trial_limit_reached",
		Data:    map[string]int{"limit": limit, "used": used},
	}
}

// 400 Bad Request
var (
	ErrInvalidRequestBody = newError(http.StatusBadRequest, "invalid request body")
	ErrInvalidPatchOp     = newError(http.StatusBadRequest, "invalid patch operation")
	ErrEmptyRequestBody   = newError(http.StatusBadRequest, "request body is empty")
	ErrNilRequestBody     = newError(http.StatusBadRequest, "request body is nil")
	ErrInvalidCountry     = newError(http.StatusBadRequest, "country is not a supported location")
	// ErrTooManyCompetitors is returned when a patch would push the competitor
	// count above the configured onboarding.maxCompetitors limit.
	ErrTooManyCompetitors = newError(http.StatusBadRequest, "competitor limit reached")
	// ErrAtLeastOneCompetitor is returned when a patch would leave the WebEntity
	// with no competitors — at least one is required for keyword overlap analysis.
	ErrAtLeastOneCompetitor = newError(http.StatusBadRequest, "at least one competitor is required")
	// ErrAtLeastOneKeyFeature is returned when a patch would leave the business
	// context with no key features — content generation prompts rely on them.
	ErrAtLeastOneKeyFeature = newError(http.StatusBadRequest, "at least one key feature is required")
	// ErrCollectionListRejected is returned when the publishing platform refused
	// the caller-supplied credentials while listing CMS collections — the fix is
	// on the user's side (API token / project URL), so a retry won't help.
	ErrCollectionListRejected = newError(http.StatusBadRequest, "could not list collections — check your API token and project URL")
	// ErrSchemaFetchRejected is returned when the publishing platform refused
	// the SAVED credentials while fetching the collection schema — the config
	// needs fixing in Settings, a retry won't help.
	ErrSchemaFetchRejected = newError(http.StatusBadRequest, "could not read your CMS collection — check your publishing settings")
)

// 413 Payload Too Large
var (
	// ErrRequestBodyTooLarge is returned when a JSON request body exceeds the
	// decode cap, before it is read into memory in full.
	ErrRequestBodyTooLarge = newError(http.StatusRequestEntityTooLarge, "request body too large")
)

// 401 Unauthorized
var (
	ErrInvalidOrExpiredToken = newError(http.StatusUnauthorized, "invalid or expired token")
)

// 404 Not Found
var (
	ErrUserNotFound      = newError(http.StatusNotFound, "user not found")
	ErrWebEntityNotFound = newError(http.StatusNotFound, "web entity not found")
)

// 403 Forbidden
var (
	// ErrAdminAccessRequired is returned to an authenticated caller whose email
	// is not on the admin allowlist. A plain 403, not a 404 masquerade: the
	// /v1/admin prefix is guessable anyway and a clear error is worth more to
	// us than obscurity.
	ErrAdminAccessRequired = newError(http.StatusForbidden, "admin access required")
	ErrWebEntityFinalised  = newError(http.StatusForbidden, "web entity is finalised and cannot be modified")
)

// 404 Not Found (site intelligence)
var (
	ErrWebEntityContextNotFound = newError(http.StatusNotFound, "web entity context not found")
	ErrScheduledArticleNotFound = newError(http.StatusNotFound, "scheduled article not found")
	ErrArticleNotGenerated      = newError(http.StatusNotFound, "article has not been generated yet")
	// ErrManualKeywordNoData is returned when DataForSEO has no row matching the
	// user-typed keyword, so there is no volume/difficulty data to enrich it.
	ErrManualKeywordNoData = newError(http.StatusNotFound, "no keyword data found for the provided keyword")
)

// 409 Conflict
var (
	ErrSiteIntelligenceNotReady = newError(http.StatusConflict, "site intelligence pipeline has not completed clustering")
	// ErrManualKeywordDuplicate is returned when the keyword already exists in
	// the WebEntityContext (case-insensitive match).
	ErrManualKeywordDuplicate = newError(http.StatusConflict, "keyword already exists for this web entity")
	// ErrScheduledArticleNotEditable is returned by the dashboard edit endpoint
	// when the article's status (generating / published / scheduling) forbids
	// edits. Past and future unpublished rows remain editable.
	ErrScheduledArticleNotEditable = newError(http.StatusConflict, "scheduled article is not editable")
	// ErrSchedulingNotComplete is returned by the admin scheduling-rerun
	// endpoint when the context's previous scheduling run hasn't finished — a
	// rerun appends to a completed calendar, never races an in-flight one.
	ErrSchedulingNotComplete = newError(http.StatusConflict, "scheduling has not completed for this context")
	// ErrInvalidScheduleDate is returned for malformed schedule date strings
	// (not YYYY-MM-DD). Past calendar dates are accepted for create and edit.
	ErrInvalidScheduleDate = newError(http.StatusBadRequest, "invalid schedule date")
	// ErrInvalidArticleType is returned when a dashboard edit specifies an
	// article type that doesn't match the canonical AllArticleTypes set.
	ErrInvalidArticleType = newError(http.StatusBadRequest, "invalid article type")
)

// 405 Method Not Allowed
var (
	ErrMethodNotAllowed = newError(http.StatusMethodNotAllowed, "method not allowed")

	// --- company tenancy (tenancy plan P4/P6) ---

	// ErrCompanyFeatureDisabled: the company surface is switched off
	// (values.yaml company.enabled). A deliberate 404 masquerade — unlike the
	// admin gate's honest 403 — so a dark feature is indistinguishable from a
	// route that doesn't exist, while keeping the JSON envelope + CORS headers
	// the raw mux 404 would drop.
	ErrCompanyFeatureDisabled = newError(http.StatusNotFound, "not found")

	// --- analytics (GSC analytics layer) ---

	// ErrAnalyticsFeatureDisabled: the analytics surface is switched off
	// (values.yaml gsc.enabled). Same 404 masquerade as the company gate.
	ErrAnalyticsFeatureDisabled = newError(http.StatusNotFound, "not found")
	// ErrAnalyticsSourceUnknown: the requested connection source isn't a
	// registered analytics source.
	ErrAnalyticsSourceUnknown = newError(http.StatusBadRequest, "unknown analytics source")

	// --- style replication ---

	// ErrStyleReplicationFeatureDisabled: the style replication surface is
	// switched off (values.yaml styleReplication.enabled). Same 404 masquerade
	// as the company/analytics gates.
	ErrStyleReplicationFeatureDisabled = newError(http.StatusNotFound, "not found")
	// ErrStyleRunActive (409): the web entity already has an in-flight run.
	ErrStyleRunActive = newError(http.StatusConflict, "a style replication run is already active")
	// ErrStyleRunRateLimited (429): the entity hit its daily run limit.
	ErrStyleRunRateLimited = newError(http.StatusTooManyRequests, "daily style replication run limit reached")
	// ErrStyleRunNotFound (404): no run in the state the call requires.
	ErrStyleRunNotFound = newError(http.StatusNotFound, "no style replication run found")
	// ErrStyleRunWrongState (409): the run isn't awaiting this action
	// (double-submit or a stale client).
	ErrStyleRunWrongState = newError(http.StatusConflict, "style replication run is not awaiting this action")
	// ErrStyleReplicationFailed (500): generic style replication failure.
	ErrStyleReplicationFailed = newError(http.StatusInternalServerError, "style replication request failed")
	// ErrGSCNoPropertyMatch (422): sites.list had no property matching the
	// site — the user hasn't added our service account yet (or added it on a
	// different property). The settings card shows "add the SA and re-check".
	ErrGSCNoPropertyMatch = newError(http.StatusUnprocessableEntity, "no matching Search Console property — add the service account to your property and re-check")
	// ErrGSCNotConfigured (503): the deployment has no service-account
	// credential; verification cannot run anywhere.
	ErrGSCNotConfigured = newError(http.StatusServiceUnavailable, "Search Console integration is not configured on this server")
	ErrAnalyticsFailed  = newError(http.StatusInternalServerError, "analytics request failed")

	ErrCompanyNotFound = newError(http.StatusNotFound, "company not found")
	// ErrCompanyPermissionDenied is the company-axis 403 — resolved through
	// EffectiveCompanyPermissions only, never the global RBAC resolver (D17).
	ErrCompanyPermissionDenied = newError(http.StatusForbidden, "you don't have permission to do that in this company")
	ErrCompanyCheckFailed      = newError(http.StatusInternalServerError, "failed to resolve company context")
	// ErrNoFreeCompanySeat (409): every purchased seat is occupied (invited +
	// claimed both hold a slot) — buy seats first, then invite (§6 ordering).
	ErrNoFreeCompanySeat  = newError(http.StatusConflict, "no free seats — purchase more seats before inviting")
	ErrMembershipNotFound = newError(http.StatusNotFound, "membership not found in this company")
	// ErrCompanyInviteInvalid is the SINGLE answer for an unknown, expired,
	// already-claimed or revoked accept token — one uniform response so the
	// link leaks nothing about which case it hit (mirrors ErrConnectLinkInvalid).
	ErrCompanyInviteInvalid = newError(http.StatusGone, "this invite link is invalid or has expired")
	// ErrInviteEmailMismatch (403): the authenticated account's verified email
	// is not the one the invite was issued to (D19).
	ErrInviteEmailMismatch  = newError(http.StatusForbidden, "this invite was sent to a different email address")
	ErrMemberAlreadyClaimed = newError(http.StatusConflict, "that email already holds a claimed membership in this company")
	// ErrOwnerMembershipLocked (409): the owner can never be archived or
	// demoted below user_admin — transfer ownership first (D12/D21).
	ErrOwnerMembershipLocked = newError(http.StatusConflict, "the owner's membership cannot be removed or demoted — transfer ownership first")
	ErrNotCompanyOwner       = newError(http.StatusForbidden, "only the company owner can do that")
	ErrTransferTargetInvalid = newError(http.StatusBadRequest, "ownership transfer target must be a claimed company admin")
	ErrCompanyDomainTaken    = newError(http.StatusConflict, "that domain is already claimed by another company")
	ErrInvalidCompanyRole    = newError(http.StatusBadRequest, "invalid company role")
	ErrPersonalCompanyLocked = newError(http.StatusConflict, "personal companies don't support that operation")
	// ErrCompanyWebsiteRequired (400): D14 — the website URL is the company
	// learn source and must be set before the company-plan purchase.
	ErrCompanyWebsiteRequired = newError(http.StatusBadRequest, "set the company website URL before purchasing a plan")
)

// --- audit (SEO/AEO audit engine) ---
var (
	// ErrAuditFeatureDisabled: the audit surface is switched off (values.yaml
	// audit.enabled). Same 404 masquerade as the company/analytics/style
	// gates.
	ErrAuditFeatureDisabled = newError(http.StatusNotFound, "not found")
	// ErrAuditRunActive (409): the web entity already has an in-flight audit.
	ErrAuditRunActive = newError(http.StatusConflict, "an audit is already running for this website")
	// ErrAuditFreeLimitReached (402, code trial_limit_reached shape): the
	// free/trial one-audit-per-website-per-email allowance is spent — the FE
	// renders the upgrade CTA.
	ErrAuditFreeLimitReached = &Error{
		Code:    http.StatusPaymentRequired,
		Message: "the free audit for this website has already been used",
		ErrCode: "audit_free_limit_reached",
	}
	// ErrAuditCooldown (429): the paid weekly per-website cadence hasn't
	// elapsed.
	ErrAuditCooldown = newError(http.StatusTooManyRequests, "this website was audited within the last week — try again later")
	// ErrAuditInvalidInput (400): malformed email/body on the lead entry.
	ErrAuditInvalidInput = newError(http.StatusBadRequest, "invalid audit request")
	// ErrAuditInvalidTarget (400): the target URL failed SSRF-safe
	// validation (scheme/port/userinfo/IP-literal/private-range rules).
	ErrAuditInvalidTarget = newError(http.StatusBadRequest, "that URL can't be audited — submit a public website address")
	// ErrAuditRateLimited (429): the lead per-IP daily cap.
	ErrAuditRateLimited = newError(http.StatusTooManyRequests, "audit rate limit reached — try again tomorrow")
	// ErrAuditDomainCooldown (429): a lead run already audited this domain
	// this week ("recent report exists" copy).
	ErrAuditDomainCooldown = newError(http.StatusTooManyRequests, "this website was audited recently — a fresh report is available weekly")
	// ErrAuditCapacity (503): the global daily lead circuit breaker tripped.
	ErrAuditCapacity = newError(http.StatusServiceUnavailable, "free audits are at capacity for today — try again tomorrow")
	// ErrAuditReportNotFound (404): no run/report matches the query (also
	// the uniform poll-token miss — no enumeration signal).
	ErrAuditReportNotFound = newError(http.StatusNotFound, "audit report not found")
	// ErrAuditRunNotFailed (409): admin resume requested on a run that isn't
	// in the terminal Error state.
	ErrAuditRunNotFailed = newError(http.StatusConflict, "the audit run is not in a failed state")
	// ErrAuditRecheckIneligible (400): the check can't be re-verified on its
	// own — the next audit measures it.
	ErrAuditRecheckIneligible = newError(http.StatusBadRequest, "this item can't be re-checked on its own — it's verified on your next audit")
	// ErrAuditRecheckStale (409): re-check targets a run that isn't the
	// latest completed audit.
	ErrAuditRecheckStale = newError(http.StatusConflict, "re-check is only available on the latest completed audit")
	// ErrAuditRecheckCooldown (429): the per-check cooldown hasn't elapsed.
	ErrAuditRecheckCooldown = newError(http.StatusTooManyRequests, "just checked — wait a few minutes before re-checking this item")
	// ErrAuditRecheckLimitReached (429): the per-run daily re-check cap.
	ErrAuditRecheckLimitReached = newError(http.StatusTooManyRequests, "daily re-check limit reached for this audit — try again tomorrow")
	// ErrAuditFailed (500): generic audit request failure.
	ErrAuditFailed = newError(http.StatusInternalServerError, "audit request failed")
)

// Payments
var (
	// ErrSubscriptionRequired (402) gates premium routes; the frontend's
	// shared fetcher branches on the "subscription_required" code before
	// redirecting to the payments page (feature_not_included 402s must not
	// hit the paywall).
	ErrSubscriptionRequired = &Error{
		Code:    http.StatusPaymentRequired,
		Message: "active subscription required",
		ErrCode: "subscription_required",
	}
	ErrSubscriptionCheckFailed = newError(http.StatusInternalServerError, "failed to verify subscription")
	ErrSubscriptionConflict    = newError(http.StatusConflict, "subscription already active")
	ErrSubscriptionNotFound    = newError(http.StatusNotFound, "no subscription found")
	ErrPaymentProviderFailed   = newError(http.StatusBadGateway, "payment provider request failed")
	// ErrUserEmailRequired covers JWT-bootstrapped users whose Clerk email
	// fetch failed — Paddle rejects customers without an email.
	ErrUserEmailRequired = newError(http.StatusBadRequest, "a verified email is required before subscribing")
	// ErrTrialNotAvailable rejects start-trial against an unknown price or a
	// plan that offers no card-less trial (trial_days unset).
	ErrTrialNotAvailable = newError(http.StatusBadRequest, "this plan does not offer a free trial")
	// ErrTeamPlanNotAvailable (400): PADDLE_TEAM_PRODUCT_ID is unset — team
	// seat billing ships dark until the Paddle product exists. The company
	// checkout guard is the single enforcement point (decision 7); no Paddle
	// call is ever attempted while unconfigured.
	ErrTeamPlanNotAvailable = newError(http.StatusBadRequest, "the team plan is not available yet")
	// ErrTrialAlreadyUsed (409, code trial_already_used): the identity has
	// prior subscription history, so the frontend should route straight to
	// paid checkout instead of retrying the trial.
	ErrTrialAlreadyUsed = &Error{
		Code:    http.StatusConflict,
		Message: "free trial already used",
		ErrCode: "trial_already_used",
	}
	// ErrTrialNotConvertible: a card-less local trial has no payment method on
	// file, so "end trial → convert" cannot bill it — the user must check out.
	ErrTrialNotConvertible = newError(http.StatusConflict, "card-less trial has no payment method; the user must check out")
	// ErrOnboardingIncomplete (409, code onboarding_incomplete): start-trial
	// before the web entity is finalised — the frontend routes back to
	// onboarding.
	ErrOnboardingIncomplete = &Error{
		Code:    http.StatusConflict,
		Message: "finish onboarding before starting a trial",
		ErrCode: "onboarding_incomplete",
	}
)

// Entitlements admin surface
var (
	// ErrUnknownFeatureKey rejects override writes containing keys not in the
	// entitlements constants — a typo'd revoke that silently no-ops would
	// leave an admin believing access was removed.
	ErrUnknownFeatureKey = newError(http.StatusBadRequest, "unknown feature key")
	// ErrEntitlementsConflict (409) means expectedUpdatedAt didn't match —
	// another admin edited the overrides since they were read.
	ErrEntitlementsConflict = newError(http.StatusConflict, "entitlements changed since last read — refetch and retry")
	// ErrTrialNotFound: the target has no trialing subscription for the app.
	ErrTrialNotFound = newError(http.StatusNotFound, "no trialing subscription found")
	// ErrInvalidPlanDoc rejects admin plan writes that fail validation
	// (missing fields, duplicate or Paddle-unknown price ids).
	ErrInvalidPlanDoc = newError(http.StatusBadRequest, "invalid plan document")
)

// Roles / RBAC admin surface
var (
	// ErrUnknownPermissionKey rejects role/grant writes containing keys not in
	// the authz permission constants — a typo'd grant that silently no-ops
	// would leave staff without access they believe they have.
	ErrUnknownPermissionKey = newError(http.StatusBadRequest, "unknown permission key")
	// ErrInvalidRoleDoc rejects role catalog writes that fail structural
	// validation (empty key, invalid permission entry).
	ErrInvalidRoleDoc = newError(http.StatusBadRequest, "invalid role document")
	// ErrRoleNotFound: the assignment/delete target names a role key that does
	// not exist in the catalog.
	ErrRoleNotFound = newError(http.StatusNotFound, "role not found")
	// ErrRolesConflict (409): a role/grants/catalog write's optimistic-
	// concurrency precondition (updated_at / role_updated_at) didn't match —
	// another admin changed it since it was read.
	ErrRolesConflict = newError(http.StatusConflict, "role changed since last read — refetch and retry")
	// ErrRoleEscalation (403) covers every privilege-escalation guard: assigning
	// a role at/above your own rank, granting a permission you don't hold, or
	// placing a superuser-tier / admin.access key where it isn't allowed. A plain
	// 403 — the caller reached the panel but overreached this specific action.
	ErrRoleEscalation = newError(http.StatusForbidden, "insufficient privilege for this role change")
	// ErrImmutableRole (403): editing the rank/permissions of an immutable
	// system role (user/superuser), or deleting any system role.
	ErrImmutableRole = newError(http.StatusForbidden, "this role is protected and cannot be modified")
	// ErrRoleInUse (409): a role delete was refused because active users still
	// hold it.
	ErrRoleInUse = newError(http.StatusConflict, "role is still assigned to one or more users")
	// ErrLastSuperuser (409): a change would drop the count of active superusers
	// to zero — the recovery-holder lockout guard. Recover via rolesmigrate.
	ErrLastSuperuser = newError(http.StatusConflict, "cannot remove the last superuser")
	// ErrSelfDeletion (409): an admin tried to delete their own account through
	// the admin surface.
	ErrSelfDeletion = newError(http.StatusConflict, "cannot delete your own account")
	// ErrSuperuserUndeletable (403): superuser accounts cannot be deleted —
	// demote the role first, where the last-superuser guard already applies.
	ErrSuperuserUndeletable = newError(http.StatusForbidden, "superusers cannot be deleted — demote the role first")
)

// 500 Internal Server Error
var (
	// ErrAdminCheckFailed covers infrastructure failures on the admin surface —
	// the authorization middleware running without a user in context (a wiring
	// bug) or a DB failure while resolving the target user. Never a 403: these
	// are not authorization verdicts.
	ErrAdminCheckFailed                 = newError(http.StatusInternalServerError, "failed to verify admin request")
	ErrLLMServiceUnavailable            = newError(http.StatusInternalServerError, "LLM service not available")
	ErrLLMProcessingFailed              = newError(http.StatusInternalServerError, "failed to process LLM response")
	ErrWebEntityCreationFailed          = newError(http.StatusInternalServerError, "failed to create web entity")
	ErrWebEntityProcessingFailed        = newError(http.StatusInternalServerError, "failed to process onboarded user")
	ErrCompetitorInfoRetrievalFailed    = newError(http.StatusInternalServerError, "failed to retrieve competitor info")
	ErrWebEntityPatchFailed             = newError(http.StatusInternalServerError, "failed to patch web entity")
	ErrSiteIntelligenceProcessingFailed = newError(http.StatusInternalServerError, "failed to process site intelligence")
	ErrContentGenerationFailed          = newError(http.StatusInternalServerError, "content generation failed")
	ErrScheduledArticlesRetrievalFailed = newError(http.StatusInternalServerError, "failed to retrieve scheduled articles")
	ErrPublishDispatchFailed            = newError(http.StatusInternalServerError, "failed to dispatch publish")
	ErrSchedulingDispatchFailed         = newError(http.StatusInternalServerError, "failed to dispatch scheduling rerun")
	// ErrCollectionListFailed (502) covers transient failures while listing CMS
	// collections — sidecar unreachable, platform down — where retrying can help.
	ErrCollectionListFailed = newError(http.StatusBadGateway, "failed to list collections — try again")
	// ErrSchemaFetchFailed (502) covers transient failures while fetching the
	// configured collection's schema — sidecar unreachable, platform down.
	ErrSchemaFetchFailed = newError(http.StatusBadGateway, "failed to read your CMS collection — try again")
)
