package finder

import (
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Per-IP rate limit for the public finder routes: an in-memory token bucket
// per client (one budget across all finder routes), so it is per instance.
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
	// sweepEvery is how often idle buckets are dropped.
	sweepEvery = time.Minute
	// maxBuckets bounds memory: past it (after a sweep) new clients are
	// refused until old buckets refill and are swept.
	maxBuckets = 100_000
)

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	ratePerSec float64
	burst      float64
	trustProxy bool
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newLimiter(perMin, burst int, trustProxy bool) *limiter {
	return &limiter{ratePerSec: float64(perMin) / 60, burst: float64(burst), trustProxy: trustProxy,
		now: time.Now, buckets: map[string]*bucket{}}
}

// limiterFromEnv reads FINDER_RATE_PER_MIN, FINDER_RATE_BURST and
// FINDER_TRUST_PROXY; a missing or invalid value uses the default.
func limiterFromEnv() *limiter {
	perMin := envPositiveInt("FINDER_RATE_PER_MIN", defaultRatePerMin)
	burst := envPositiveInt("FINDER_RATE_BURST", defaultRateBurst)
	trust, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("FINDER_TRUST_PROXY")))
	return newLimiter(perMin, burst, trust)
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

// allow takes one token from key's bucket; when empty it returns how long
// until the next token.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.sweep(now)
		}
		if len(l.buckets) >= maxBuckets {
			return false, l.perToken()
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.ratePerSec)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.ratePerSec * float64(time.Second))
}

// sweep drops buckets idle long enough to have refilled completely: they
// are identical to a fresh bucket, so forgetting them changes nothing.
func (l *limiter) sweep(now time.Time) {
	l.lastSweep = now
	full := time.Duration(l.burst / l.ratePerSec * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}

func (l *limiter) perToken() time.Duration {
	return time.Duration(float64(time.Second) / l.ratePerSec)
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// middleware answers 429 (with Retry-After, in whole seconds) once the
// client's bucket is empty. Chain it inside AllowCORS so preflights are free
// and a 429 still carries the CORS headers.
func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.allow(clientKey(r, l.trustProxy))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusTooManyRequests,
				Message: "Too many requests. Please wait a moment and try again.", ErrCode: "rate_limited"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey is the bucket key: the client IP, from the connection unless
// trustProxy (then CF-Connecting-IP, else the right-most X-Forwarded-For
// entry, i.e. the one the nearest proxy appended). IPv6 is grouped by /64,
// which one client can rotate through freely.
func clientKey(r *http.Request, trustProxy bool) string {
	var ip net.IP
	if trustProxy {
		ip = net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP")))
		if ip == nil {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				ip = net.ParseIP(strings.TrimSpace(parts[len(parts)-1]))
			}
		}
	}
	if ip == nil {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip = net.ParseIP(host)
	}
	switch {
	case ip == nil:
		return r.RemoteAddr
	case ip.To4() != nil:
		return ip.To4().String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}
