package apperrors

import "net/http"

// AppError pairs an HTTP status code with a user-facing message.
type Error struct {
	Code    int
	Message string
	// ErrCode is a machine-readable discriminator clients branch on when two
	// errors share an HTTP status. Empty = omitted from the wire.
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

// 400 Bad Request
var (
	ErrInvalidRequestBody = newError(http.StatusBadRequest, "invalid request body")
	ErrInvalidPatchOp     = newError(http.StatusBadRequest, "invalid patch operation")
	ErrEmptyRequestBody   = newError(http.StatusBadRequest, "request body is empty")
	ErrNilRequestBody     = newError(http.StatusBadRequest, "request body is nil")
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
	ErrUserNotFound = newError(http.StatusNotFound, "user not found")
)

// 403 Forbidden
var (
	// ErrAdminAccessRequired is returned to an authenticated caller who lacks
	// the admin permission a route needs. A plain 403, not a 404 masquerade:
	// the /v1/admin prefix is guessable anyway and a clear error is worth more
	// than obscurity.
	ErrAdminAccessRequired = newError(http.StatusForbidden, "admin access required")
)

// 405 Method Not Allowed
var (
	ErrMethodNotAllowed = newError(http.StatusMethodNotAllowed, "method not allowed")
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
	ErrAdminCheckFailed      = newError(http.StatusInternalServerError, "failed to verify admin request")
	ErrLLMServiceUnavailable = newError(http.StatusInternalServerError, "LLM service not available")
	ErrLLMProcessingFailed   = newError(http.StatusInternalServerError, "failed to process LLM response")
	// ErrDispatchFailed covers a failure to enqueue async work.
	ErrDispatchFailed = newError(http.StatusInternalServerError, "failed to dispatch job")
)
