package ratelimit

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// Headers the frontend's server-side proxy (Next.js on Vercel) sets on every
// call it makes for a visitor. Every such call reaches crunch from Vercel's
// IP, so without them all visitors would share one bucket. The names must
// match warehousehub-frontend src/core/api/client-ip.ts.
const (
	HeaderClientIP    = "X-WH-Client-IP"
	HeaderProxySecret = "X-WH-Proxy-Secret"
)

// trustedProxyKey is the bucket for calls from the trusted proxy that carry
// no visitor IP (server work with no visitor behind it, e.g. the sitemap).
const trustedProxyKey = "trusted-proxy"

// Resolver turns a request into its bucket key: the real client IP.
//
//  1. ProxySecret set and the request carries it (X-WH-Proxy-Secret):
//     X-WH-Client-IP is trusted. Without a valid IP it is the trusted-proxy
//     bucket. Anyone else sending X-WH-Client-IP is ignored.
//  2. TrustProxyHeaders: CF-Connecting-IP, else the right-most
//     X-Forwarded-For entry (the one the nearest proxy appended). Set it only
//     behind a proxy that sets those headers, or clients can spoof them.
//  3. Otherwise the connection's address.
//
// IPv6 is grouped by /64, which one client can rotate through freely.
type Resolver struct {
	ProxySecret       string
	TrustProxyHeaders bool
}

// Key is a KeyFunc.
func (res Resolver) Key(r *http.Request) string {
	if res.fromTrustedProxy(r) {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(HeaderClientIP))); ip != nil {
			return normalize(ip)
		}
		return trustedProxyKey
	}
	var ip net.IP
	if res.TrustProxyHeaders {
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
	if ip == nil {
		return r.RemoteAddr
	}
	return normalize(ip)
}

// fromTrustedProxy reports whether the request carries the shared proxy
// secret (constant-time compare; an unset secret trusts nobody).
func (res Resolver) fromTrustedProxy(r *http.Request) bool {
	if res.ProxySecret == "" {
		return false
	}
	got := r.Header.Get(HeaderProxySecret)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(res.ProxySecret)) == 1
}

func normalize(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}
