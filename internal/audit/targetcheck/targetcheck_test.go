package targetcheck

import (
	"context"
	"net"
	"strings"
	"testing"
)

// The bypass corpus (port of url_safety.py's intent). Every case here
// rejects BEFORE DNS resolution, so the table stays hermetic; the
// metadata-IP-behind-DNS cases are covered by disallowedIPReason below.
func TestValidate_RejectionCorpus(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"file scheme", "file:///etc/passwd"},
		{"gopher scheme", "gopher://example.com"},
		{"mixed-case bad scheme", "JavaScript:alert(1)"},
		{"userinfo trick", "https://user:pass@example.com"},
		{"userinfo-at confusion", "https://trusted.com@evil.com/path"},
		{"port game 8080", "https://example.com:8080"},
		{"port game 22", "https://example.com:22"},
		{"ipv4 literal", "https://93.184.216.34"},
		{"metadata ip literal", "http://169.254.169.254/latest/meta-data/"},
		{"ipv6 literal", "https://[::1]"},
		{"decimal ip literal", "https://2130706433"},
		{"localhost", "https://localhost"},
		{"localhost subdomain", "https://foo.localhost"},
		{"dot-local", "https://printer.local"},
		{"dot-internal", "https://db.internal"},
		{"invented TLD", "https://foo.notarealtldzzz"},
		{"reserved invalid TLD", "https://foo.invalid"},
		{"bare public suffix", "https://com"},
		{"too long", "https://example.com/" + strings.Repeat("a", 3000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Validate(context.Background(), tc.raw); err == nil {
				t.Errorf("Validate(%q) accepted — must reject", tc.raw)
			}
		})
	}
}

// TestValidate_NormalizesSchemelessInput exercises the pre-DNS normalization
// path: a schemeless input must not fail on parsing (it may still fail on
// DNS in an offline environment, which is fine — we only assert it never
// fails with a scheme error).
func TestValidate_NormalizesSchemelessInput(t *testing.T) {
	_, _, err := Validate(context.Background(), "example.com/blog")
	if err != nil && strings.Contains(err.Error(), "scheme") {
		t.Errorf("schemeless input tripped the scheme check: %v", err)
	}
}

func TestDisallowedIPReason(t *testing.T) {
	cases := []struct {
		ip     string
		reject bool
	}{
		{"127.0.0.1", true},           // loopback
		{"10.1.2.3", true},            // RFC1918
		{"172.16.0.1", true},          // RFC1918
		{"192.168.1.1", true},         // RFC1918
		{"169.254.169.254", true},     // link-local / cloud metadata
		{"100.64.0.1", true},          // CGNAT
		{"0.0.0.0", true},             // unspecified
		{"224.0.0.1", true},           // multicast
		{"::1", true},                 // v6 loopback
		{"fe80::1", true},             // v6 link-local
		{"fd00::1", true},             // v6 unique-local
		{"93.184.216.34", false},      // public v4
		{"2606:2800:220:1::1", false}, // public v6
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			reason := disallowedIPReason(parseIP(t, tc.ip))
			if tc.reject && reason == "" {
				t.Errorf("%s must be rejected", tc.ip)
			}
			if !tc.reject && reason != "" {
				t.Errorf("%s wrongly rejected as %s", tc.ip, reason)
			}
		})
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test ip %q", s)
	}
	return ip
}
