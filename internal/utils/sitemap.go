package utils

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// sitemapUserAgent is sent on every sitemap request. Many production sites and
// CDNs (Cloudflare et al.) reject Go's default "Go-http-client/1.1" agent with a
// 403, which would silently zero out sitemap discovery — so we present as a
// real browser.
const sitemapUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// SitemapEntry is one <url> row from a sitemap: the page URL plus its optional
// <lastmod> timestamp (zero when the sitemap doesn't carry one). HasPriority /
// HasChangefreq report whether the entry emitted those fields — Google ignores
// both, so the audit reports them as noise.
type SitemapEntry struct {
	URL           string
	LastMod       time.Time
	HasPriority   bool
	HasChangefreq bool
}

// SitemapFetchOptions bounds a sitemap crawl. Defined here (mapped from the
// caller's config values) rather than importing config, which pulls in the
// service root packages.
type SitemapFetchOptions struct {
	// FetchTimeout bounds each sitemap HTTP call.
	FetchTimeout time.Duration
	// MaxBytes caps a single sitemap document's body.
	MaxBytes int64
	// MaxURLs caps how many entries are returned.
	MaxURLs int
	// ExtraCandidates are tried BEFORE the conventional locations —
	// robots.txt-declared sitemap URLs go here, which is how WordPress's
	// default /wp-sitemap.xml (not in the conventional list) gets found.
	ExtraCandidates []string
}

// FetchSitemapEntries tries the common sitemap locations for a site and returns
// up to opts.MaxURLs page entries (URL + lastmod). Sitemap-index files are
// followed one level deep. Returns nil on any failure — callers treat that as
// "no sitemap available".
func FetchSitemapEntries(ctx context.Context, websiteURL string, opts SitemapFetchOptions) []SitemapEntry {
	entries, _ := FetchSitemapEntriesWithSource(ctx, websiteURL, opts)
	return entries
}

// FetchSitemapEntriesWithSource additionally reports WHICH candidate URL the
// entries came from, so callers never cite a sitemap location that 404s.
func FetchSitemapEntriesWithSource(ctx context.Context, websiteURL string, opts SitemapFetchOptions) ([]SitemapEntry, string) {
	candidates := append(append([]string{}, opts.ExtraCandidates...), CandidateSitemapURLs(websiteURL)...)
	if len(candidates) == 0 {
		log.Warn("sitemap: no candidates derived from website URL", "websiteURL", websiteURL)
		return nil, ""
	}

	client := &http.Client{Timeout: opts.FetchTimeout}

	tried := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || tried[candidate] {
			continue
		}
		tried[candidate] = true
		body, err := FetchURLBody(ctx, client, candidate, opts.MaxBytes)
		if err != nil {
			log.Debug("sitemap: candidate fetch failed", "candidate", candidate, "error", err)
			continue
		}
		entries, childSitemaps := ParseSitemapXML(body)
		log.Debug("sitemap: parsed candidate", "candidate", candidate, "entries", len(entries), "childSitemaps", len(childSitemaps))

		// Sitemap index — pull child sitemaps one level deep.
		for _, child := range childSitemaps {
			if len(entries) >= opts.MaxURLs {
				break
			}
			childBody, cErr := FetchURLBody(ctx, client, child, opts.MaxBytes)
			if cErr != nil {
				continue
			}
			childEntries, _ := ParseSitemapXML(childBody)
			entries = append(entries, childEntries...)
		}

		if len(entries) > 0 {
			return capEntries(dedupeEntries(entries), opts.MaxURLs), candidate
		}
	}

	log.Warn("sitemap: no URLs found across candidates", "websiteURL", websiteURL, "candidates", len(candidates))
	return nil, ""
}

// FetchSitemapURLs is the URL-only view of FetchSitemapEntries, for callers
// that don't care about <lastmod> (internal link insertion).
func FetchSitemapURLs(ctx context.Context, websiteURL string, opts SitemapFetchOptions) []string {
	entries := FetchSitemapEntries(ctx, websiteURL, opts)
	if len(entries) == 0 {
		return nil
	}
	urls := make([]string, 0, len(entries))
	for _, e := range entries {
		urls = append(urls, e.URL)
	}
	return urls
}

// CandidateSitemapURLs derives the standard sitemap locations from a site's
// base URL. Returns nil when the URL can't be parsed into a host.
func CandidateSitemapURLs(websiteURL string) []string {
	raw := strings.TrimSpace(websiteURL)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	return []string{
		origin + "/sitemap.xml",
		origin + "/sitemap_index.xml",
		origin + "/post-sitemap.xml",
		origin + "/page-sitemap.xml",
	}
}

// sitemapLoc / sitemapDoc decode both <urlset> and <sitemapindex> documents.
// encoding/xml matches child element local names regardless of namespace, so a
// single struct covers both forms.
type sitemapLoc struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod"`
	Priority   string `xml:"priority"`
	ChangeFreq string `xml:"changefreq"`
}

type sitemapDoc struct {
	URLs     []sitemapLoc `xml:"url"`
	Sitemaps []sitemapLoc `xml:"sitemap"`
}

// sitemapLastModLayouts are the W3C Datetime forms sitemaps use for <lastmod>:
// full RFC3339 down to a bare date. Tried in order; unparseable values yield a
// zero LastMod rather than dropping the entry.
var sitemapLastModLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04Z07:00",
	"2006-01-02",
}

func parseSitemapLastMod(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range sitemapLastModLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseSitemapXML returns the page entries (<url><loc> + optional <lastmod>)
// and any child sitemap URLs (<sitemap><loc>) found in a sitemap document.
// Malformed XML yields empties.
func ParseSitemapXML(data []byte) (entries []SitemapEntry, childSitemaps []string) {
	var doc sitemapDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, nil
	}
	for _, u := range doc.URLs {
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			entries = append(entries, SitemapEntry{
				URL:           loc,
				LastMod:       parseSitemapLastMod(u.LastMod),
				HasPriority:   strings.TrimSpace(u.Priority) != "",
				HasChangefreq: strings.TrimSpace(u.ChangeFreq) != "",
			})
		}
	}
	for _, sm := range doc.Sitemaps {
		if loc := strings.TrimSpace(sm.Loc); loc != "" {
			childSitemaps = append(childSitemaps, loc)
		}
	}
	return entries, childSitemaps
}

// FetchURLBody GETs a URL with the given client and returns its body, capped at
// maxBytes. Non-200 responses are treated as errors.
func FetchURLBody(ctx context.Context, client *http.Client, target string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// Present as a browser; default Go agent is frequently 403'd by CDNs/WAFs.
	req.Header.Set("User-Agent", sitemapUserAgent)
	req.Header.Set("Accept", "application/xml,text/xml,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap fetch %s: status %d", target, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// DedupeURLs removes duplicate URLs preserving first-seen order.
func DedupeURLs(urls []string) []string {
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

// CapURLs truncates the slice to at most max entries.
func CapURLs(urls []string, max int) []string {
	if len(urls) > max {
		return urls[:max]
	}
	return urls
}

func dedupeEntries(entries []SitemapEntry) []SitemapEntry {
	seen := make(map[string]struct{}, len(entries))
	out := make([]SitemapEntry, 0, len(entries))
	for _, e := range entries {
		if _, ok := seen[e.URL]; ok {
			continue
		}
		seen[e.URL] = struct{}{}
		out = append(out, e)
	}
	return out
}

func capEntries(entries []SitemapEntry, max int) []SitemapEntry {
	if len(entries) > max {
		return entries[:max]
	}
	return entries
}
