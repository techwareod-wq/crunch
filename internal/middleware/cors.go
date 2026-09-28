package middleware

import "net/http"

const (
	corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowedHeaders = "Content-Type, Authorization, X-Admin-Action"
)

// CORS origin allowlist, configured once at boot via SetAllowedOrigins. When
// no origins are configured (or "*" is listed), CORS stays fully permissive —
// the previous behaviour — so local/dev setups keep working without config.
var (
	allowedOrigins  = map[string]struct{}{}
	allowAllOrigins = true
)

// SetAllowedOrigins locks CORS to an explicit list of origins. Call once at
// startup, before serving. An empty list (or one containing "*") leaves CORS
// permissive. Origins must be exact scheme+host[+port] strings, e.g.
// "https://app.example.com" — they are matched against the request Origin
// header verbatim.
func SetAllowedOrigins(origins []string) {
	allowedOrigins = make(map[string]struct{}, len(origins))
	allowAllOrigins = len(origins) == 0
	for _, o := range origins {
		if o == "*" {
			allowAllOrigins = true
			continue
		}
		allowedOrigins[o] = struct{}{}
	}
}

// resolveAllowOrigin returns the value to send in Access-Control-Allow-Origin
// for the given request Origin, and whether to emit the header at all. In
// allowlist mode it reflects the request origin only when it matches (never a
// wildcard, so the response stays valid for future credentialed requests); an
// unlisted origin gets no header and the browser blocks the response.
func resolveAllowOrigin(origin string) (string, bool) {
	if allowAllOrigins {
		return "*", true
	}
	if origin == "" {
		return "", false
	}
	if _, ok := allowedOrigins[origin]; ok {
		return origin, true
	}
	return "", false
}

// AllowCORS wraps the handler with CORS headers and handles preflight requests.
// The allowed origin is resolved per-request against the configured allowlist
// (see SetAllowedOrigins).
func (p pattern) AllowCORS() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowOrigin, ok := resolveAllowOrigin(r.Header.Get(HeaderOrigin)); ok {
			w.Header().Set(HeaderAccessControlAllowOrigin, allowOrigin)
			// Reflecting a per-origin value means caches must key on Origin, or
			// they could serve one origin's CORS headers to another.
			if allowOrigin != "*" {
				w.Header().Add(HeaderVary, HeaderOrigin)
			}
		}
		w.Header().Set(HeaderAccessControlAllowMethods, corsAllowedMethods)
		w.Header().Set(HeaderAccessControlAllowHeaders, corsAllowedHeaders)
		w.Header().Set(HeaderAccessControlMaxAge, corsMaxAge)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ro.ServeHTTP(w, r)
	})
	return p
}
