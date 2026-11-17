// Package ratelimit is the per-client, in-memory token-bucket limiter used by
// the anonymous /v1/public routes (search, listing, finder). Buckets live in
// process memory, so limits are per instance.
package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
)

const (
	// sweepEvery is how often idle buckets are dropped.
	sweepEvery = time.Minute
	// maxBuckets bounds memory: past it (after a sweep) new clients are
	// refused until old buckets refill and are swept.
	maxBuckets = 100_000
)

// KeyFunc picks the bucket a request draws from (normally Resolver.Key).
type KeyFunc func(*http.Request) string

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a token bucket per key: Burst requests at once, refilled at
// PerMin per minute. One Limiter is one shared budget across every route it
// is chained on.
type Limiter struct {
	ratePerSec float64
	burst      float64
	key        KeyFunc
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// New returns a limiter of perMin sustained requests per minute with a
// bucket of burst, keyed by key. Non-positive values are clamped to 1.
func New(perMin, burst int, key KeyFunc) *Limiter {
	return &Limiter{
		ratePerSec: float64(max(1, perMin)) / 60,
		burst:      float64(max(1, burst)),
		key:        key,
		now:        time.Now,
		buckets:    map[string]*bucket{},
	}
}

// Allow takes one token from key's bucket; when empty it returns how long
// until the next token.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
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
func (l *Limiter) sweep(now time.Time) {
	l.lastSweep = now
	full := time.Duration(l.burst / l.ratePerSec * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) perToken() time.Duration {
	return time.Duration(float64(time.Second) / l.ratePerSec)
}

func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Middleware answers 429 (with Retry-After, in whole seconds) once the
// client's bucket is empty. Chain it inside AllowCORS so preflights are free
// and a 429 still carries the CORS headers.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.Allow(l.key(r))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusTooManyRequests,
				Message: "Too many requests. Please wait a moment and try again.", ErrCode: "rate_limited"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
