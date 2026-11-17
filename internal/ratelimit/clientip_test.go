package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolverKey(t *testing.T) {
	const secret = "s3cret-s3cret-s3cret-s3cret-s3cret"
	req := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	spoof := map[string]string{"CF-Connecting-IP": "9.9.9.9", "X-Forwarded-For": "8.8.8.8"}
	proxied := func(ip, sec string) map[string]string {
		return map[string]string{HeaderClientIP: ip, HeaderProxySecret: sec, "CF-Connecting-IP": "76.76.21.21"}
	}
	for name, tc := range map[string]struct {
		r    *http.Request
		res  Resolver
		want string
	}{
		"connection ip":             {req("198.51.100.7:4000", nil), Resolver{}, "198.51.100.7"},
		"headers ignored untrusted": {req("198.51.100.7:4000", spoof), Resolver{}, "198.51.100.7"},
		"cf header trusted":         {req("127.0.0.1:4000", spoof), Resolver{TrustProxyHeaders: true}, "9.9.9.9"},
		"xff right-most trusted":    {req("127.0.0.1:4000", map[string]string{"X-Forwarded-For": "1.1.1.1, 2.2.2.2"}), Resolver{TrustProxyHeaders: true}, "2.2.2.2"},
		"garbage header falls back": {req("127.0.0.1:4000", map[string]string{"CF-Connecting-IP": "nope", "X-Forwarded-For": "x"}), Resolver{TrustProxyHeaders: true}, "127.0.0.1"},
		"ipv6 grouped by /64":       {req("[2001:db8:1:2:aaaa::1]:4000", nil), Resolver{}, "2001:db8:1:2::"},
		"ipv6 same /64 same key":    {req("[2001:db8:1:2:bbbb::9]:4000", nil), Resolver{}, "2001:db8:1:2::"},

		// Trusted proxy (the Next.js server on Vercel).
		"proxy client ip wins over cf":   {req("127.0.0.1:4000", proxied("203.0.113.5", secret)), Resolver{ProxySecret: secret, TrustProxyHeaders: true}, "203.0.113.5"},
		"proxy client ipv6 grouped":      {req("127.0.0.1:4000", proxied("2001:db8:9:9::42", secret)), Resolver{ProxySecret: secret}, "2001:db8:9:9::"},
		"proxy without client ip":        {req("127.0.0.1:4000", proxied("", secret)), Resolver{ProxySecret: secret}, trustedProxyKey},
		"proxy with garbage client ip":   {req("127.0.0.1:4000", proxied("not-an-ip", secret)), Resolver{ProxySecret: secret}, trustedProxyKey},
		"wrong secret ignored":           {req("127.0.0.1:4000", proxied("203.0.113.5", "wrong")), Resolver{ProxySecret: secret, TrustProxyHeaders: true}, "76.76.21.21"},
		"wrong secret, untrusted":        {req("198.51.100.7:4000", proxied("203.0.113.5", "wrong")), Resolver{ProxySecret: secret}, "198.51.100.7"},
		"no secret configured trusts no": {req("198.51.100.7:4000", proxied("203.0.113.5", "")), Resolver{}, "198.51.100.7"},
		"empty secret header ignored":    {req("198.51.100.7:4000", map[string]string{HeaderClientIP: "203.0.113.5"}), Resolver{ProxySecret: secret}, "198.51.100.7"},
	} {
		if got := tc.res.Key(tc.r); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
