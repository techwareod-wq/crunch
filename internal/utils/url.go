package utils

import (
	"net/url"
	"strings"
)

func CleanURL(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimRight(url, "/")
	return url
}

// NormalizeGSCPageURL canonicalizes a page URL for Search Console matching:
// scheme collapsed to https (GSC reports http:// and https:// variants of one
// page as separate rows — they must share one fact identity so the analytics
// collision merge can fold them, LLD §3.1a), host lowercased, fragment and
// query string stripped, trailing slash stripped (root "/" kept), leading
// "www." collapsed (real subdomains kept — changing this collapse set changes
// gsc fact identity and requires a full-history replay of the source).
// The SAME function is applied to GSC page rows at ingest and to our
// published URLs at publish-stamp time, so equality is symmetric — never
// normalize one side with anything else. A scheme-less input ("abc.com/blog")
// is treated as https; anything without a parseable http(s) host returns "".
func NormalizeGSCPageURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	scheme = "https"
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	host := strings.ToLower(u.Host)
	// Collapse the www label like the scheme: Google reports the SERVING
	// variant (useindexly.com) while user-entered site URLs often carry www —
	// both must land on one fact identity. Real subdomains (clerk., blog.)
	// stay distinct.
	host = strings.TrimPrefix(host, "www.")
	return scheme + "://" + host + path
}

// ExtractDomain reduces a website URL to its bare registrable host: no scheme,
// no "www." prefix, no userinfo/port, no path, query, fragment, or trailing
// slash — e.g. "https://www.abc.com/blog/" -> "abc.com", "PQR.ai/" -> "pqr.ai".
// DataForSEO's backlinks summary keys on a domain target, so anything beyond the
// host would narrow or invalidate the lookup.
func ExtractDomain(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if i := strings.Index(s, "://"); i != -1 {
		s = s[i+3:]
	}
	// Keep only the host segment, dropping path/query/fragment.
	if i := strings.IndexAny(s, "/?#"); i != -1 {
		s = s[:i]
	}
	// Drop any userinfo ("user:pass@") and port.
	if i := strings.LastIndex(s, "@"); i != -1 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i != -1 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.ToLower(s)
}

// BrandFromURL reduces a website URL to its brand token: the registrable
// domain's second-level label — e.g. "https://www.hubspot.com/pricing" ->
// "hubspot", "app.notion.so" -> "app". Used to build a "%brand%" not_like filter
// that drops a domain's own navigational/branded keywords. Returns "" when no
// host is present, in which case the caller skips the brand filter (a "%%"
// pattern would match — and exclude — everything).
func BrandFromURL(rawURL string) string {
	domain := ExtractDomain(rawURL)
	if i := strings.Index(domain, "."); i != -1 {
		return domain[:i]
	}
	return domain
}
