// Package targetcheck is the SSRF-safe validation gate for user-supplied
// audit targets (a port of claude-seo's url_safety.py INTENT — the surface
// shrinks since DataForSEO/PSI do most fetching, but
// utils.fetchRenderedHTML falls back to a DIRECT in-process fetch, which
// makes this gate mandatory, not defense-in-depth). Applied at both tenant
// and lead starts; lead runs re-validate at crawl time (DNS-rebinding
// posture: validate close to use).
package targetcheck

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// maxTargetURLLength bounds a submitted target URL.
const maxTargetURLLength = 2048

// Validate normalizes and validates a user-supplied target. On success it
// returns the cleaned absolute URL and the normalized registrable domain
// (eTLD+1 — the rate-limit key). Every rejection reason is a plain error the
// caller wraps into ErrAuditInvalidTarget.
func Validate(ctx context.Context, raw string) (targetURL, targetDomain string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty target URL")
	}
	if len(raw) > maxTargetURLLength {
		return "", "", fmt.Errorf("target URL exceeds %d characters", maxTargetURLLength)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("unparseable target URL")
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("scheme %q is not allowed", u.Scheme)
	}
	if u.User != nil {
		return "", "", fmt.Errorf("userinfo in the target URL is not allowed")
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return "", "", fmt.Errorf("port %s is not allowed", port)
	}

	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", "", fmt.Errorf("target URL has no host")
	}
	// IP-literal targets are rejected outright — audits are for domains.
	if ip := net.ParseIP(host); ip != nil {
		return "", "", fmt.Errorf("IP-literal targets are not allowed")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "", "", fmt.Errorf("internal hostnames are not allowed")
	}

	// Public-suffix sanity: the host must have a registrable eTLD+1 under a
	// REAL (ICANN) public suffix — "foo.bar" with an invented TLD fails.
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", "", fmt.Errorf("host has no registrable domain")
	}
	if suffix, icann := publicsuffix.PublicSuffix(host); !icann || suffix == host {
		return "", "", fmt.Errorf("host is not under a public suffix")
	}

	// Resolve and reject every private/special range — the direct-fetch
	// fallback would otherwise reach it from inside the VPC.
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return "", "", fmt.Errorf("hostname does not resolve")
	}
	for _, addr := range addrs {
		if reason := disallowedIPReason(addr.IP); reason != "" {
			return "", "", fmt.Errorf("hostname resolves to a %s address", reason)
		}
	}

	// Normalize: scheme+host+path, no fragment, query dropped (an audit
	// targets a site, not a session).
	clean := &url.URL{Scheme: scheme, Host: strings.ToLower(u.Host), Path: u.Path}
	return strings.TrimSuffix(clean.String(), "/"), etld1, nil
}

// disallowedIPReason classifies IPs the audit must never touch: loopback,
// RFC1918 private, link-local (the 169.254.169.254 cloud-metadata endpoint
// lives here), CGNAT, unspecified, multicast, and the v6 equivalents.
func disallowedIPReason(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	}
	// CGNAT 100.64.0.0/10.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return "carrier-grade NAT"
	}
	// v6 unique-local fc00::/7.
	if v4 := ip.To4(); v4 == nil && len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return "unique-local"
	}
	return ""
}
