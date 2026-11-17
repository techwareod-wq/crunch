package ratelimit

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

func testLimiter(perMin, burst int, key KeyFunc) (*Limiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if key == nil {
		key = Resolver{}.Key
	}
	l := New(perMin, burst, key)
	l.now = clk.now
	return l, clk
}

func TestLimiterBurstThenRecovers(t *testing.T) {
	l, clk := testLimiter(60, 20, nil)
	for i := range 20 {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := l.Allow("1.2.3.4")
	if ok {
		t.Fatal("request past the burst allowed")
	}
	if wait != time.Second {
		t.Errorf("wait = %v, want 1s at 60/min", wait)
	}
	if ok, _ := l.Allow("5.6.7.8"); !ok {
		t.Error("another client shares the first one's bucket")
	}
	clk.add(time.Second)
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("no token after one second at 60/min")
	}
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Error("two tokens after one second at 60/min")
	}
	clk.add(time.Hour)
	for i := range 20 {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("refill capped below burst: request %d refused", i+1)
		}
	}
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Error("refill exceeded the burst")
	}
}

func TestLimiterSweepsStaleBuckets(t *testing.T) {
	l, clk := testLimiter(60, 20, nil)
	for i := range 1000 {
		l.Allow("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	if l.size() != 1000 {
		t.Fatalf("size %d", l.size())
	}
	clk.add(sweepEvery) // ≥ 20 s full refill: every bucket is stale
	l.Allow("1.1.1.1")
	if l.size() != 1 {
		t.Errorf("after the sweep %d buckets remain, want 1", l.size())
	}
}

func TestLimiterTableFull(t *testing.T) {
	l, _ := testLimiter(60, 20, nil)
	for i := range maxBuckets {
		l.buckets[strconv.Itoa(i)] = &bucket{tokens: 0, last: l.now()}
	}
	l.lastSweep = l.now()
	if ok, wait := l.Allow("new-client"); ok || wait <= 0 {
		t.Errorf("a full table refuses new clients: ok=%t wait=%v", ok, wait)
	}
	if l.size() != maxBuckets {
		t.Errorf("table grew past the cap: %d", l.size())
	}
}

func TestNewClampsNonPositive(t *testing.T) {
	l := New(0, -5, Resolver{}.Key)
	if l.ratePerSec != 1.0/60 || l.burst != 1 {
		t.Errorf("clamped: rate=%v burst=%v", l.ratePerSec, l.burst)
	}
}

func serve(h http.Handler, path, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

func TestMiddleware429(t *testing.T) {
	l, clk := testLimiter(60, 2, nil)
	h := l.Middleware(okHandler)
	call := func() *httptest.ResponseRecorder { return serve(h, "/v1/public/search", "203.0.113.9:5555", nil) }
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

// One limiter chained on several routes is one budget across them.
func TestMiddlewareSharedBudgetAcrossRoutes(t *testing.T) {
	l, _ := testLimiter(60, 3, nil)
	h := l.Middleware(okHandler)
	for _, p := range []string{"/v1/public/search", "/v1/public/catalog", "/v1/public/listing"} {
		if rec := serve(h, p, "203.0.113.9:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	if rec := serve(h, "/v1/public/search/map", "203.0.113.9:1", nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("4th call across routes: %d, want 429", rec.Code)
	}
}

// The Vercel case: every call arrives from the same proxy address. With the
// secret, each visitor gets their own bucket, so one heavy visitor can't
// lock everyone else out.
func TestMiddlewareVisitorsBehindTrustedProxy(t *testing.T) {
	res := Resolver{ProxySecret: "s3cret-s3cret-s3cret-s3cret-s3cret"}
	l, _ := testLimiter(60, 2, res.Key)
	h := l.Middleware(okHandler)
	const vercel = "76.76.21.21:443"
	visitor := func(ip string) map[string]string {
		return map[string]string{HeaderProxySecret: res.ProxySecret, HeaderClientIP: ip}
	}
	for range 2 {
		serve(h, "/v1/public/search", vercel, visitor("198.51.100.1"))
	}
	if rec := serve(h, "/v1/public/search", vercel, visitor("198.51.100.1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("heavy visitor not limited: %d", rec.Code)
	}
	if rec := serve(h, "/v1/public/search", vercel, visitor("198.51.100.2")); rec.Code != http.StatusOK {
		t.Errorf("other visitor behind the same proxy limited: %d", rec.Code)
	}
}

// A direct caller can't pick its own bucket by sending X-WH-Client-IP.
func TestMiddlewareSpoofedClientIPIgnored(t *testing.T) {
	res := Resolver{ProxySecret: "s3cret-s3cret-s3cret-s3cret-s3cret"}
	l, _ := testLimiter(60, 2, res.Key)
	h := l.Middleware(okHandler)
	for i := range 3 {
		hdr := map[string]string{HeaderClientIP: "10.0.0." + strconv.Itoa(i), HeaderProxySecret: "guess"}
		rec := serve(h, "/v1/public/search", "203.0.113.50:1", hdr)
		if i < 2 && rec.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
		if i == 2 && rec.Code != http.StatusTooManyRequests {
			t.Errorf("rotating spoofed IPs dodged the limit: %d", rec.Code)
		}
	}
}
