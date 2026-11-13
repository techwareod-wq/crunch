package aisearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func testLimiter(perMin, perDay int) (*userLimiter, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	l := newUserLimiter(perMin, perDay)
	l.now = c.now
	return l, c
}

func TestUserLimiterMinute(t *testing.T) {
	l, c := testLimiter(10, 100)
	for i := range 10 {
		if ok, _, _ := l.allow("user_a"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
	}
	ok, wait, code := l.allow("user_a")
	if ok || code != codeMinuteLimit || wait <= 0 || wait > 6*time.Second {
		t.Fatalf("11th: ok=%t wait=%v code=%q", ok, wait, code)
	}
	if ok, _, _ := l.allow("user_b"); !ok {
		t.Error("another user has their own limit")
	}
	c.add(wait)
	if ok, _, _ := l.allow("user_a"); !ok {
		t.Error("served again after Retry-After")
	}
}

func TestUserLimiterDay(t *testing.T) {
	l, c := testLimiter(10, 25)
	served := 0
	for range 60 { // 10 per minute over 6 minutes = 60 attempts
		for range 10 {
			if ok, _, _ := l.allow("u"); ok {
				served++
			}
		}
		c.add(time.Minute)
	}
	if served != 25 {
		t.Fatalf("served %d in the day, want 25", served)
	}
	ok, wait, code := l.allow("u")
	if ok || code != codeDailyLimit || wait <= 0 || wait > 24*time.Hour {
		t.Fatalf("over the day: ok=%t wait=%v code=%q", ok, wait, code)
	}
	c.add(wait)
	if ok, _, _ := l.allow("u"); !ok {
		t.Error("a new day window serves again")
	}
}

func TestUserLimiterSweep(t *testing.T) {
	l, c := testLimiter(10, 100)
	for i := range 500 {
		l.allow("u" + strconv.Itoa(i))
	}
	c.add(day)
	l.allow("fresh")
	if n := l.size(); n != 1 {
		t.Errorf("after a day %d users remain, want 1", n)
	}
}

func TestUserLimiterMiddleware(t *testing.T) {
	l, _ := testLimiter(2, 100)
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func(u *models.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/public/search/ai", nil)
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, u))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	visitor := &models.User{ClerkID: "user_visitor", Role: models.RoleUser}
	for range 2 {
		if rec := call(visitor); rec.Code != http.StatusOK {
			t.Fatalf("within limit: %d", rec.Code)
		}
	}
	rec := call(visitor)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("over limit: %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	var env struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Success || env.Code != codeMinuteLimit || env.Error == "" {
		t.Errorf("envelope = %s", rec.Body.String())
	}
	for _, staff := range []*models.User{{ClerkID: "user_admin", Role: models.RoleAdmin}, {ClerkID: "user_su", Role: models.RoleSuperuser}} {
		for range 5 {
			if rec := call(staff); rec.Code != http.StatusOK {
				t.Errorf("staff %s limited: %d", staff.Role, rec.Code)
			}
		}
	}
	if l.size() != 1 {
		t.Errorf("staff must not be tracked: %d users", l.size())
	}
	if rec := call(nil); rec.Code != http.StatusOK {
		t.Errorf("no user (route requires sign-in anyway): %d", rec.Code)
	}
}

func TestUserLimiterFromEnv(t *testing.T) {
	t.Setenv("AISEARCH_USER_PER_MIN", "3")
	t.Setenv("AISEARCH_USER_PER_DAY", "40")
	if l := userLimiterFromEnv(); l.perMin != 3 || l.perDay != 40 {
		t.Errorf("env: %d/%d", l.perMin, l.perDay)
	}
	t.Setenv("AISEARCH_USER_PER_MIN", "0")
	t.Setenv("AISEARCH_USER_PER_DAY", "many")
	if l := userLimiterFromEnv(); l.perMin != defaultUserPerMin || l.perDay != defaultUserPerDay {
		t.Errorf("invalid values fall back: %d/%d", l.perMin, l.perDay)
	}
}
