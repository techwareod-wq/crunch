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

// FeatureNotIncluded (403, code feature_not_included) — the caller has some
// access but not this feature. data.feature names it.
func FeatureNotIncluded(feature string) *Error {
	return &Error{
		Code:    http.StatusForbidden,
		Message: "you don't have access to this feature",
		ErrCode: "feature_not_included",
		Data:    map[string]string{"feature": feature},
	}
}

// 400 Bad Request
var (
	ErrInvalidRequestBody = newError(http.StatusBadRequest, "invalid request body")
	ErrInvalidPatchOp     = newError(http.StatusBadRequest, "invalid patch operation")
	ErrEmptyRequestBody   = newError(http.StatusBadRequest, "request body is empty")
	ErrNilRequestBody     = newError(http.StatusBadRequest, "request body is nil")
	// ErrInvalidPhone rejects a phone number that isn't a valid number
	// (D-100: format check only, default region IN).
	ErrInvalidPhone = &Error{Code: http.StatusBadRequest, Message: "invalid phone number", ErrCode: "invalid_phone"}
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
	// ErrNotFound is the generic 404 for a lookup by id that matched nothing.
	ErrNotFound = newError(http.StatusNotFound, "not found")
)

// 403 Forbidden
var (
	// ErrAdminAccessRequired is returned to an authenticated caller who lacks
	// the admin permission a route needs. A plain 403, not a 404 masquerade:
	// the /v1/admin prefix is guessable anyway and a clear error is worth more
	// than obscurity.
	ErrAdminAccessRequired = newError(http.StatusForbidden, "admin access required")
	// ErrAccessRequired (code access_required) — the caller holds no features
	// at all: a superuser hasn't given them access yet.
	ErrAccessRequired = &Error{Code: http.StatusForbidden, Message: "your account hasn't been given access yet", ErrCode: "access_required"}
)

// 405 Method Not Allowed
var (
	ErrMethodNotAllowed = newError(http.StatusMethodNotAllowed, "method not allowed")
)

// Admin access management
var (
	// ErrInvalidAccess: role must be user or admin; permissions any of
	// editor/approver/attributes, and none for role user.
	ErrInvalidAccess = &Error{Code: http.StatusBadRequest, Message: "role must be user or admin; permissions any of editor, approver, attributes (none for user)", ErrCode: "invalid_access"}
	// ErrAccessConflict (409): the target's access changed since it was read.
	// ErrInvalidFeatures: features must be any of the grantable features.
	ErrInvalidFeatures = &Error{Code: http.StatusBadRequest, Message: "features must be any of search, ai_search, listings, enquiries", ErrCode: "invalid_features"}
	ErrAccessConflict  = &Error{Code: http.StatusConflict, Message: "access changed since last read — refetch and retry", ErrCode: "access_conflict"}
	// ErrSuperuserImmutable (403): superuser accounts are managed only by
	// cmd/superuser, never through the API.
	ErrSuperuserImmutable = newError(http.StatusForbidden, "superuser accounts can't be changed from the panel")
	// ErrSelfDeletion (409): an admin tried to delete their own account through
	// the admin surface.
	ErrSelfDeletion = newError(http.StatusConflict, "cannot delete your own account")
	// ErrSuperuserUndeletable (403): superuser accounts cannot be deleted.
	ErrSuperuserUndeletable = newError(http.StatusForbidden, "superusers cannot be deleted")
)

// 500 Internal Server Error
var (
	// ErrAdminCheckFailed covers infrastructure failures on the admin surface —
	// the authorization middleware running without a user in context (a wiring
	// bug) or a DB failure while resolving the target user. Never a 403: these
	// are not authorization verdicts.
	ErrAdminCheckFailed = newError(http.StatusInternalServerError, "failed to verify admin request")
	// ErrFeatureCheckFailed: the feature gate ran without a user (chain-order
	// bug). Never a 403.
	ErrFeatureCheckFailed    = newError(http.StatusInternalServerError, "failed to verify access")
	ErrLLMServiceUnavailable = newError(http.StatusInternalServerError, "LLM service not available")
	ErrLLMProcessingFailed   = newError(http.StatusInternalServerError, "failed to process LLM response")
	// ErrInternal is the generic 500 for an unexpected server-side failure.
	ErrInternal = newError(http.StatusInternalServerError, "internal error")
	// ErrDispatchFailed covers a failure to enqueue async work.
	ErrDispatchFailed = newError(http.StatusInternalServerError, "failed to dispatch job")
)
