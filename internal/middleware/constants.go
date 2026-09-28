package middleware

// HTTP header names used across the middleware chain.
const (
	HeaderAuthorization             = "Authorization"
	HeaderOrigin                    = "Origin"
	HeaderVary                      = "Vary"
	HeaderAccessControlAllowOrigin  = "Access-Control-Allow-Origin"
	HeaderAccessControlAllowMethods = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowHeaders = "Access-Control-Allow-Headers"
	HeaderAccessControlMaxAge       = "Access-Control-Max-Age"
	HeaderContentType               = "Content-Type"
)

// contentTypeJSON is the Content-Type every JSON response carries.
const contentTypeJSON = "application/json"

// bearerPrefix is the (case-insensitively matched) Authorization scheme prefix.
const bearerPrefix = "bearer "

const corsMaxAge = "86400" // 24 hours in seconds

// healthPath is always served regardless of the Host allowlist so external
// health probes are not coupled to the pinned domain.
const healthPath = "/health"

// hostLocalhost always passes the host guard so in-container probes keep
// working regardless of the pinned domain.
const hostLocalhost = "localhost"
