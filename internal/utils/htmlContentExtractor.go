package utils

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ErrScrapeBlocked marks a fetch that technically succeeded but returned a
// bot-protection challenge or effectively empty page instead of content.
// Retrying from the same client/IP will almost always re-fail.
var ErrScrapeBlocked = errors.New("scrape blocked or empty")

// scrapeUserAgent impersonates current-stable Chrome on macOS so bot walls
// keyed on the default Go UA don't trip. Bump the Chrome version occasionally;
// it only needs to be plausible, not current.
const scrapeUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// scrapeTimeout bounds a single page fetch so a hung site can't stall a queue
// worker until the SQS visibility timeout.
const scrapeTimeout = 20 * time.Second

// maxScrapeBodyBytes caps how much of a response body is read before parsing,
// keeping a pathological page from eating a worker.
const maxScrapeBodyBytes = 5 << 20

// minScrapeTextChars is the least visible text a page can carry and still
// count as content. A real homepage — even a sparse one — clears this easily;
// a page under it couldn't produce usable business context anyway, so failing
// loudly beats extracting garbage. This is also what catches CSR-only sites
// (empty HTML shell) until a rendering fallback exists.
const minScrapeTextChars = 200

// challengeMarkers are case-insensitive substrings of bot-protection
// challenge pages (Cloudflare & friends), which often ship with HTTP 200.
var challengeMarkers = []string{
	"enable javascript and cookies",
	"checking your browser",
	"verify you are human",
	"just a moment",
	"attention required",
	"verifying you are not a bot",
}

// fetchHTML GETs the URL with browser-like headers and returns the parsed
// document. Returns ErrScrapeBlocked (wrapped) when the response is a
// bot-protection challenge or near-empty shell rather than real content.
func fetchHTML(pageURL string) (*html.Node, error) {
	if !strings.HasPrefix(pageURL, "http://") && !strings.HasPrefix(pageURL, "https://") {
		pageURL = "https://" + pageURL
	}

	// Fresh jar per call: some bot walls set a cookie and redirect back, and a
	// per-call jar replays it without unbounded growth or cross-site state.
	// The zero-Transport client shares http.DefaultTransport's connection pool.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}
	client := &http.Client{Timeout: scrapeTimeout, Jar: jar}

	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request for %s: %w", pageURL, err)
	}
	// No manual Accept-Encoding: leaving it unset lets Go's transport handle
	// gzip transparently.
	req.Header.Set("User-Agent", scrapeUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusServiceUnavailable:
		// The challenge/ratelimit codes bot walls use.
		return nil, fmt.Errorf("%s returned HTTP %d: %w", pageURL, resp.StatusCode, ErrScrapeBlocked)
	case resp.StatusCode >= 400:
		// e.g. a 404 homepage is "wrong URL", not "blocked".
		return nil, fmt.Errorf("%s returned HTTP %d", pageURL, resp.StatusCode)
	}

	doc, err := html.Parse(io.LimitReader(resp.Body, maxScrapeBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML from %s: %w", pageURL, err)
	}

	if err := validateScrapedDoc(doc); err != nil {
		return nil, fmt.Errorf("%s: %w", pageURL, err)
	}

	return doc, nil
}

// validateScrapedDoc rejects documents that parsed fine but are not real
// content: bot-protection challenge pages served with HTTP 200, and
// near-empty shells (CSR-only sites, parked domains).
func validateScrapedDoc(doc *html.Node) error {
	text := visibleText(doc)
	lower := strings.ToLower(text)
	for _, marker := range challengeMarkers {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("challenge marker %q in page text: %w", marker, ErrScrapeBlocked)
		}
	}
	if len(text) < minScrapeTextChars {
		return fmt.Errorf("page has %d chars of visible text (min %d): %w", len(text), minScrapeTextChars, ErrScrapeBlocked)
	}
	return nil
}

// visibleText returns the document's text as a browser user would see it,
// without mutating the tree (callers still need meta tags afterwards).
// script/style are skipped so a JS bundle can't pass the min-text check, and
// noscript so a legit page's "please enable JavaScript" fallback can't trip
// the challenge markers.
func visibleText(n *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				buf.WriteString(t + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(buf.String())
}

// ArticleStructure holds the structural elements extracted from a competitor
// blog post. Used by the CEP SERP scrape step (Step 2a) to analyse the top
// ranking articles without relying on any external scraping service.
type ArticleStructure struct {
	H1              string
	H2s             []string
	H3s             []string
	WordCount       int
	MetaDescription string
}

// ExtractArticleStructure fetches a URL and returns its heading hierarchy,
// approximate word count, and meta description. Suitable for parallel calls
// via errgroup — each invocation is fully independent.
func ExtractArticleStructure(url string) (*ArticleStructure, error) {
	doc, err := fetchHTML(url)
	if err != nil {
		return nil, err
	}

	result := &ArticleStructure{}

	result.MetaDescription = extractMetaDescription(doc)

	extractHeadings(doc, result)
	result.WordCount = countWords(doc)

	return result, nil
}

func extractMetaDescription(n *html.Node) string {
	var walk func(*html.Node) (string, bool)
	walk = func(n *html.Node) (string, bool) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			var name, content string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "name":
					name = strings.ToLower(strings.TrimSpace(a.Val))
				case "content":
					content = a.Val
				}
			}
			if name == "description" && content != "" {
				return content, true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v, ok := walk(c); ok {
				return v, true
			}
		}
		return "", false
	}
	desc, _ := walk(n)
	return desc
}

func extractHeadings(n *html.Node, result *ArticleStructure) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h1":
				if result.H1 == "" {
					result.H1 = strings.TrimSpace(nodeText(n))
				}
			case "h2":
				if text := strings.TrimSpace(nodeText(n)); text != "" {
					result.H2s = append(result.H2s, text)
				}
			case "h3":
				if text := strings.TrimSpace(nodeText(n)); text != "" {
					result.H3s = append(result.H3s, text)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
}

func nodeText(n *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			buf.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return buf.String()
}

// countWords returns an approximate word count for the page. It prefers the
// <main> or <article> element for a more accurate article-only count; if
// neither exists it falls back to the full document.
func countWords(doc *html.Node) int {
	target := FindContentNode(doc)
	if target == nil {
		target = doc
	}
	return len(strings.Fields(ExtractText(target)))
}

// FindContentNode searches for a <main> or <article> element, which most
// modern blog platforms use to wrap primary content. Returns nil when the page
// has neither — callers fall back to the whole document.
func FindContentNode(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && (n.Data == "main" || n.Data == "article") {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := FindContentNode(c); found != nil {
			return found
		}
	}
	return nil
}

// ExtractArticleBodyText returns the page's readable body text, preferring the
// content node (FindContentNode) and falling back to the whole document.
// script/style/noscript text never leaks in (visibleText skips them), so this
// is safe on an un-stripped tree. The result is capped at maxChars on a word
// boundary (0 = uncapped).
func ExtractArticleBodyText(doc *html.Node, maxChars int) string {
	target := FindContentNode(doc)
	if target == nil {
		target = doc
	}
	text := visibleText(target)
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	truncated := text[:maxChars]
	if idx := strings.LastIndex(truncated, " "); idx > 0 {
		truncated = truncated[:idx]
	}
	return strings.TrimSpace(truncated)
}

func FetchWebsiteContent(website string, extractWithHtmlTags bool, removeComments bool) (string, error) {
	doc, err := fetchHTML(website)
	if err != nil {
		return "", err
	}
	return renderDocContent(doc, extractWithHtmlTags, removeComments)
}

// renderDocContent is the shared post-fetch processing for every website-content
// path (direct fetch and DataForSEO-rendered): strip non-content nodes, then
// return either the remaining markup or plain text.
func renderDocContent(doc *html.Node, extractWithHtmlTags bool, removeComments bool) (string, error) {
	RemoveNodes(doc, removeComments)

	if extractWithHtmlTags {
		var buf strings.Builder
		if err := html.Render(&buf, doc); err != nil {
			return "", fmt.Errorf("failed to render HTML: %w", err)
		}
		return buf.String(), nil
	}

	return ExtractText(doc), nil
}

func RemoveNodes(n *html.Node, removeComments bool) {
	var toRemove []*html.Node

	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "meta" || n.Data == "link") {
			toRemove = append(toRemove, n)
			return
		}
		if removeComments && n.Type == html.CommentNode {
			toRemove = append(toRemove, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)

	for _, node := range toRemove {
		node.Parent.RemoveChild(node)
	}
}

func ExtractText(n *html.Node) string {
	var buf strings.Builder

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.TextNode {
			if text := strings.TrimSpace(n.Data); text != "" {
				buf.WriteString(text + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(n)

	return strings.TrimSpace(buf.String())
}
