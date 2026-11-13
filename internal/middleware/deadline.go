package middleware

import (
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// ExtendWriteDeadline gives one route a longer write deadline than the
// server-wide WriteTimeout (http.Server), for handlers that can legitimately
// run longer (e.g. bulk approve). Chain it outside any middleware that wraps
// the ResponseWriter (after WithAdminAuthorization, whose audit recorder does
// not expose the connection), so http.ResponseController reaches it. If the
// deadline can't be set the request still runs under the server timeout.
func ExtendWriteDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
				log.Warn("could not extend write deadline", "path", r.URL.Path, "error", err)
			}
			next.ServeHTTP(w, r)
		})
	}
}
