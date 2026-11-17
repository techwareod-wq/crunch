package finder

import (
	"os"
	"strconv"
	"strings"

	"github.com/atharva-ng/crunch/internal/ratelimit"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Per-IP rate limit for the public finder routes: its own token bucket per
// client (one budget across all finder routes), in memory, per instance.
// The client IP is resolved by ratelimit.Resolver: the shared proxy secret
// (PROXY_SHARED_SECRET) makes the frontend proxy's X-WH-Client-IP trusted.
//
//	FINDER_RATE_PER_MIN  sustained requests per minute (default 60)
//	FINDER_RATE_BURST    bucket size (default 20)
//	FINDER_TRUST_PROXY   "true": take the client IP from CF-Connecting-IP /
//	                     X-Forwarded-For. Only behind a proxy that sets them
//	                     (e.g. a Cloudflare tunnel); otherwise anyone can
//	                     spoof the headers to dodge the limit.

const (
	defaultRatePerMin = 60
	defaultRateBurst  = 20
)

type limitSettings struct {
	perMin, burst int
	trustProxy    bool
}

// settingsFromEnv reads FINDER_RATE_PER_MIN, FINDER_RATE_BURST and
// FINDER_TRUST_PROXY; a missing or invalid value uses the default.
func settingsFromEnv() limitSettings {
	trust, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("FINDER_TRUST_PROXY")))
	return limitSettings{
		perMin:     envPositiveInt("FINDER_RATE_PER_MIN", defaultRatePerMin),
		burst:      envPositiveInt("FINDER_RATE_BURST", defaultRateBurst),
		trustProxy: trust,
	}
}

func limiterFromEnv(proxySecret string) *ratelimit.Limiter {
	s := settingsFromEnv()
	res := ratelimit.Resolver{ProxySecret: proxySecret, TrustProxyHeaders: s.trustProxy}
	return ratelimit.New(s.perMin, s.burst, res.Key)
}

func envPositiveInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Warn("finder: ignoring invalid rate limit setting", "name", name, "value", raw)
		return def
	}
	return n
}
