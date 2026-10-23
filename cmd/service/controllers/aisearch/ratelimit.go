package aisearch

import (
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Per-user limit on POST /v1/public/search/ai (each call can spend LLM
// tokens): a per-minute token bucket plus a rolling 24-hour count, keyed by
// the Clerk user id, in memory (per instance). Staff are exempt.
//
//	AISEARCH_USER_PER_MIN  requests per minute (default 10; also the burst)
//	AISEARCH_USER_PER_DAY  requests per 24 hours (default 100)

const (
	defaultUserPerMin = 10
	defaultUserPerDay = 100
	day               = 24 * time.Hour
	// sweepEvery is how often finished windows are dropped; maxUsers bounds
	// memory (new users are refused past it until a sweep frees room).
	sweepEvery = time.Minute
	maxUsers   = 100_000
)

// Error codes, so the frontend can show the right message.
const (
	codeMinuteLimit = "ai_rate_limited"
	codeDailyLimit  = "ai_daily_limit"
)

type usage struct {
	tokens   float64
	last     time.Time
	dayStart time.Time
	dayCount int
}

type userLimiter struct {
	perMin, perDay int
	now            func() time.Time

	mu        sync.Mutex
	users     map[string]*usage
	lastSweep time.Time
}

func newUserLimiter(perMin, perDay int) *userLimiter {
	return &userLimiter{perMin: perMin, perDay: perDay, now: time.Now, users: map[string]*usage{}}
}

// userLimiterFromEnv reads AISEARCH_USER_PER_MIN / AISEARCH_USER_PER_DAY;
// unset or invalid uses the default.
func userLimiterFromEnv() *userLimiter {
	return newUserLimiter(positiveInt("AISEARCH_USER_PER_MIN", os.Getenv("AISEARCH_USER_PER_MIN"), defaultUserPerMin),
		positiveInt("AISEARCH_USER_PER_DAY", os.Getenv("AISEARCH_USER_PER_DAY"), defaultUserPerDay))
}

func positiveInt(name, raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Warn("aisearch: ignoring invalid rate limit setting", "name", name, "value", raw)
		return def
	}
	return n
}

// allow counts one request for id. When refused it returns the wait and
// the error code (minute or daily limit).
func (l *userLimiter) allow(id string) (bool, time.Duration, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	u, ok := l.users[id]
	if !ok {
		if len(l.users) >= maxUsers {
			l.sweep(now)
		}
		if len(l.users) >= maxUsers {
			return false, time.Minute, codeMinuteLimit
		}
		u = &usage{tokens: float64(l.perMin), last: now, dayStart: now}
		l.users[id] = u
	}
	if now.Sub(u.dayStart) >= day {
		u.dayStart, u.dayCount = now, 0
	}
	rate := float64(l.perMin) / 60 // tokens per second
	u.tokens = math.Min(float64(l.perMin), u.tokens+now.Sub(u.last).Seconds()*rate)
	u.last = now
	switch {
	case u.dayCount >= l.perDay:
		return false, u.dayStart.Add(day).Sub(now), codeDailyLimit
	case u.tokens < 1:
		return false, time.Duration((1 - u.tokens) / rate * float64(time.Second)), codeMinuteLimit
	}
	u.tokens--
	u.dayCount++
	return true, 0, ""
}

// sweep drops users whose day window ended and whose bucket refilled: they
// are identical to a new user.
func (l *userLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for id, u := range l.users {
		if now.Sub(u.dayStart) >= day && now.Sub(u.last) >= time.Minute {
			delete(l.users, id)
		}
	}
}

func (l *userLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.users)
}

// isStaff matches viewerOf: any role other than a visitor.
func isStaff(u *models.User) bool { return u.Role != "" && u.Role != models.RoleUser }

// userKey is the Clerk user id (the Mongo id when it is missing).
func userKey(u *models.User) string {
	if u.ClerkID != "" {
		return u.ClerkID
	}
	return u.ID.Hex()
}

// middleware answers 429 with Retry-After once the caller's limit is hit.
// Chain it inside JWT authentication and the feature check (so a refused
// sign-in or feature doesn't spend quota).
func (l *userLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := middleware.GetOptionalUserFromContext(r)
		if u == nil || isStaff(u) {
			next.ServeHTTP(w, r)
			return
		}
		ok, wait, code := l.allow(userKey(u))
		if !ok {
			msg := "You're searching quickly. Please wait a moment and try again."
			if code == codeDailyLimit {
				msg = "You've reached today's AI search limit. Try again later or use the filters."
			}
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusTooManyRequests, Message: msg, ErrCode: code})
			return
		}
		next.ServeHTTP(w, r)
	})
}
