package checks

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

// maxFindingPages caps the affected-URL list on one finding — the report
// names enough pages to act on, not the whole crawl.
const maxFindingPages = 10

// capPages bounds a finding's page list. It also drops exact-duplicate URLs
// (first occurrence wins) — a page can never be "affected twice", and the FE
// keys page rows by URL.
func capPages(pages []string) []string {
	seen := make(map[string]struct{}, len(pages))
	out := pages[:0:0]
	for _, p := range pages {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) == maxFindingPages {
			break
		}
	}
	return out
}

// ratioScore builds the standard pages-passing score: passing/total applied
// to a 100-point possible. total == 0 means "nothing to assess" — full marks,
// because absence of assessable surface is not a defect (checks that treat
// absence AS the defect score explicitly instead).
func ratioScore(passing, total int) *core.Score {
	if total <= 0 {
		return &core.Score{Earned: 100, Possible: 100}
	}
	return &core.Score{Earned: 100 * float64(passing) / float64(total), Possible: 100}
}

// fixedScore builds an explicit earned/100 score.
func fixedScore(earned float64) *core.Score {
	if earned < 0 {
		earned = 0
	}
	if earned > 100 {
		earned = 100
	}
	return &core.Score{Earned: earned, Possible: 100}
}

// urlClusterKey groups a URL into its template cluster ("/blogs/foo" →
// "blogs/*"); "" for the root and single-segment pages (mirrors the crawl
// collector's sampling clusters).
func urlClusterKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || segs[0] == "" {
		return ""
	}
	return strings.ToLower(segs[0]) + "/*"
}

// templateScaleNote states the blast radius of a per-template defect: when an
// affected sampled page belongs to a large crawled URL cluster, the same
// template ships the defect to every sibling. Empty when no crawl artifact is
// available or no affected page sits in a cluster.
func templateScaleNote(in Input, affected []string) string {
	crawl, ok := in.Bundle.Crawl()
	if !ok {
		return ""
	}
	sizes := map[string]int{}
	for _, p := range crawl.Pages {
		if p.StatusCode != 200 || p.IsRedirect {
			continue
		}
		if key := urlClusterKey(p.URL); key != "" {
			sizes[key]++
		}
	}
	var notes []string
	seen := map[string]bool{}
	for _, u := range affected {
		key := urlClusterKey(u)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if n := sizes[key]; n >= 3 {
			notes = append(notes, fmt.Sprintf("%s is one of %d crawled pages sharing the /%s template — expect the same defect across that whole section", u, n, strings.TrimSuffix(key, "/*")))
		}
		if len(notes) == 2 {
			break
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "Note: " + strings.Join(notes, "; ") + "."
}

// normalizeCheckURL canonicalizes a URL for equality comparison: scheme +
// host lowercased, www. and default ports stripped, trailing-slash-insensitive
// path, query kept, fragment dropped. Unparseable input falls back to a
// trimmed lowercase compare.
func normalizeCheckURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	scheme := strings.ToLower(u.Scheme)
	if port := u.Port(); port != "" &&
		!((scheme == "https" && port == "443") || (scheme == "http" && port == "80")) {
		host += ":" + port
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	out := scheme + "://" + host + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}
