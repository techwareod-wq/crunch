package config

import "testing"

func TestLoadRateLimitConfig(t *testing.T) {
	t.Setenv("PROXY_SHARED_SECRET", "  0123456789abcdef0123456789abcdef  ")
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	t.Setenv("PUBLIC_RATE_PER_MIN", "300")
	t.Setenv("PUBLIC_RATE_BURST", "90")
	var c AppConfig
	c.LoadRateLimitConfig()
	want := RateLimitConfig{ProxySecret: "0123456789abcdef0123456789abcdef", TrustProxyHeaders: true, PublicPerMin: 300, PublicBurst: 90}
	if c.RateLimit != want {
		t.Errorf("got %+v, want %+v", c.RateLimit, want)
	}

	t.Setenv("PROXY_SHARED_SECRET", "")
	t.Setenv("TRUST_PROXY_HEADERS", "")
	t.Setenv("PUBLIC_RATE_PER_MIN", "-1")
	t.Setenv("PUBLIC_RATE_BURST", "lots")
	c = AppConfig{}
	c.LoadRateLimitConfig()
	want = RateLimitConfig{PublicPerMin: DefaultPublicRatePerMin, PublicBurst: DefaultPublicRateBurst}
	if c.RateLimit != want {
		t.Errorf("defaults: got %+v, want %+v", c.RateLimit, want)
	}
}
