package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// Host allowlist, configured once at boot via SetAllowedHosts. When empty the
// guard is disabled and every Host is served — the default for local/dev and
// any environment that does not pin a domain. Production pins
// "api.useindexly.com" so requests sent straight to the server's IP (whose Host
// header is the IP, not the domain) are rejected.
var allowedHosts = map[string]struct{}{}

// SetAllowedHosts locks the server to an explicit set of Host header values.
// Call once at startup, before serving. An empty list disables the guard.
// Entries are matched case-insensitively against the request Host with any
// port stripped, e.g. "api.useindexly.com".
func SetAllowedHosts(hosts []string) {
	allowedHosts = make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allowedHosts[h] = struct{}{}
		}
	}
}

// hostAllowed reports whether the given request Host (possibly "host:port") may
// be served. Loopback hosts always pass so in-container probes — notably the
// Docker HEALTHCHECK hitting 127.0.0.1 — keep working regardless of the pinned
// domain.
func hostAllowed(requestHost string) bool {
	host := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	if host == hostLocalhost {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}

	_, ok := allowedHosts[host]
	return ok
}

// HostGuard wraps the server's top-level handler and rejects requests whose Host
// header is not in the allowlist (see SetAllowedHosts). It is a no-op when no
// hosts are configured. The /health path is always served so external health
// probes are not coupled to the pinned domain.
func HostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowedHosts) == 0 || r.URL.Path == healthPath || hostAllowed(r.Host) {
			next.ServeHTTP(w, r)
			return
		}

		log.Warn("rejected request with disallowed host",
			"host", r.Host,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
	})
}
