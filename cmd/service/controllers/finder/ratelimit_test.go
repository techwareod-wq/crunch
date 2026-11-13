package finder

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fakeClock is a settable now().
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func testLimiter(perMin, burst int) (*limiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newLimiter(perMin, burst, false)
	l.now = clk.now
	return l, clk
}

func TestLimiterBurstThenRecovers(t *testing.T) {
	l, clk := testLimiter(60, 20)
	for i := range 20 {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := l.allow("1.2.3.4")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("21st: ok=%t wait=%v; want refused, ~1s", ok, wait)
	}
	if ok, _ := l.allow("5.6.7.8"); !ok {
		t.Error("another client has its own bucket")
	}
	clk.add(wait)
	if ok, _ := l.allow("1.2.3.4"); !ok {
		t.Error("after Retry-After the client is served again")
	}
	if ok, _ := l.allow("1.2.3.4"); ok {
		t.Error("only one token refilled")
	}
	clk.add(time.Minute)
	for i := range 20 {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("after a minute the burst is back: request %d refused", i+1)
		}
	}
}

func TestLimiterSweepsStaleBuckets(t *testing.T) {
	l, clk := testLimiter(60, 20)
	for i := range 1000 {
		l.allow("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	if l.size() != 1000 {
		t.Fatalf("size %d", l.size())
	}
	clk.add(sweepEvery) // ≥ 20 s full refill: every bucket is stale
	l.allow("1.1.1.1")
	if l.size() != 1 {
		t.Errorf("after the sweep %d buckets remain, want 1", l.size())
	}
}

func TestLimiterTableFull(t *testing.T) {
	l, _ := testLimiter(60, 20)
	for i := range maxBuckets {
		l.buckets[strconv.Itoa(i)] = &bucket{tokens: 0, last: l.now()}
	}
	l.lastSweep = l.now()
	if ok, wait := l.allow("new-client"); ok || wait <= 0 {
		t.Errorf("a full table refuses new clients: ok=%t wait=%v", ok, wait)
	}
	if l.size() != maxBuckets {
		t.Errorf("table grew past the cap: %d", l.size())
	}
}

func TestLimiterMiddleware429(t *testing.T) {
	l, clk := testLimiter(60, 2)
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/public/finder/search", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for range 2 {
		if rec := call(); rec.Code != http.StatusOK {
			t.Fatalf("within burst: %d", rec.Code)
		}
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over burst: %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want 1", ra)
	}
	var env struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Success || env.Error == "" || env.Code != "rate_limited" {
		t.Errorf("envelope = %s", rec.Body.String())
	}
	clk.add(time.Second)
	if rec := call(); rec.Code != http.StatusOK {
		t.Errorf("after waiting: %d, want 200", rec.Code)
	}
}

func TestClientKey(t *testing.T) {
	req := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	spoof := map[string]string{"CF-Connecting-IP": "9.9.9.9", "X-Forwarded-For": "8.8.8.8"}
	for name, tc := range map[string]struct {
		r     *http.Request
		trust bool
		want  string
	}{
		"connection ip":             {req("198.51.100.7:4000", nil), false, "198.51.100.7"},
		"headers ignored untrusted": {req("198.51.100.7:4000", spoof), false, "198.51.100.7"},
		"cf header trusted":         {req("127.0.0.1:4000", spoof), true, "9.9.9.9"},
		"xff right-most trusted":    {req("127.0.0.1:4000", map[string]string{"X-Forwarded-For": "1.1.1.1, 2.2.2.2"}), true, "2.2.2.2"},
		"garbage header falls back": {req("127.0.0.1:4000", map[string]string{"CF-Connecting-IP": "nope", "X-Forwarded-For": "x"}), true, "127.0.0.1"},
		"ipv6 grouped by /64":       {req("[2001:db8:1:2:aaaa::1]:4000", nil), false, "2001:db8:1:2::"},
		"ipv6 same /64 same key":    {req("[2001:db8:1:2:bbbb::9]:4000", nil), false, "2001:db8:1:2::"},
	} {
		if got := clientKey(tc.r, tc.trust); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestLimiterFromEnv(t *testing.T) {
	t.Setenv("FINDER_RATE_PER_MIN", "120")
	t.Setenv("FINDER_RATE_BURST", "5")
	t.Setenv("FINDER_TRUST_PROXY", "true")
	if l := limiterFromEnv(); l.ratePerSec != 2 || l.burst != 5 || !l.trustProxy {
		t.Errorf("env: %+v", l)
	}
	t.Setenv("FINDER_RATE_PER_MIN", "-3")
	t.Setenv("FINDER_RATE_BURST", "lots")
	t.Setenv("FINDER_TRUST_PROXY", "")
	if l := limiterFromEnv(); l.ratePerSec != 1 || l.burst != defaultRateBurst || l.trustProxy {
		t.Errorf("invalid values fall back to defaults: %+v", l)
	}
}
