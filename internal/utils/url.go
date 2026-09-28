package utils

import "strings"

func CleanURL(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimRight(url, "/")
	return url
}

// ExtractDomain reduces a website URL to its bare registrable host: no scheme,
// no "www." prefix, no userinfo/port, no path, query, fragment, or trailing
// slash — e.g. "https://www.abc.com/blog/" -> "abc.com", "PQR.ai/" -> "pqr.ai".
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
