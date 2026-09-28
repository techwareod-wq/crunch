package utils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
	"golang.org/x/net/html"
)

// Defaults for DataForSEOFetchValues fields left unset in values.yaml. 20000
// is the task-level status_code DataForSEO returns on success (their errors
// ride inside an HTTP 200 envelope, so the HTTP status alone proves nothing);
// 45s covers a browser-rendered crawl (routinely 15–30s) while staying below
// the shared ApiClient's cap.
const (
	defaultDataForSEOTaskOK       = 20000
	defaultDataForSEOFetchTimeout = 45 * time.Second
)

// ErrCrawlHostBusy marks DataForSEO's "duplicate crawl host" rejection:
// another crawl (an OnPage task or a concurrent instant_pages call) holds the
// host lock. Transient — the lock clears when the other crawl finishes.
var ErrCrawlHostBusy = fmt.Errorf("dataforseo crawl host busy")

// crawlHostBusyRetries/crawlHostBusyBackoff bound the retry on
// ErrCrawlHostBusy. Short on purpose: a lock held by a minutes-long OnPage
// crawl won't clear in this window — the direct fallback covers that case —
// but tail-end locks and back-to-back same-host fetches recover here.
// Backoff is a var so tests can shrink it.
const crawlHostBusyRetries = 2

var crawlHostBusyBackoff = 12 * time.Second

// DataForSEOFetchValues carries the apis.dataForSEO tunables from values.yaml.
// Defined here (mapped from config.DataForSEOValues by the caller) rather than
// importing config, which pulls in the service root packages. Zero fields fall
// back to the defaults above so a missing yaml block can't disable the primary
// fetch path.
type DataForSEOFetchValues struct {
	// TaskOKStatusCode is the task-level status_code treated as success.
	TaskOKStatusCode int
	// FetchTimeout bounds the whole rendered-fetch flow (instant_pages crawl +
	// raw_html collection); on expiry the direct fetch takes over.
	FetchTimeout time.Duration
}

func (v DataForSEOFetchValues) taskOK() int {
	if v.TaskOKStatusCode != 0 {
		return v.TaskOKStatusCode
	}
	return defaultDataForSEOTaskOK
}

func (v DataForSEOFetchValues) timeout() time.Duration {
	if v.FetchTimeout > 0 {
		return v.FetchTimeout
	}
	return defaultDataForSEOFetchTimeout
}

// FetchRenderedWebsiteContent fetches a page's content with DataForSEO's
// OnPage instant_pages crawl as the primary path and the plain in-process HTTP
// fetch (FetchWebsiteContent) as the fallback.
//
// The DataForSEO crawl runs with the full anti-blocking profile — headless
// browser rendering (executes JS, so CSR-only sites return real markup and
// JS-based bot checks pass), a real Chrome user agent, a desktop viewport
// preset, and proxy-pool switching for IP-reputation walls — which is exactly
// the traffic the direct fetch can't imitate. When that path fails for any
// reason (API error, task failure, blocked crawl, stored HTML gone), the
// direct fetch still runs, so sites that block DataForSEO's ranges but not
// ours keep working and a DataForSEO outage never takes onboarding down.
//
// The extractWithHtmlTags/removeComments semantics match FetchWebsiteContent.
// A returned error wraps the direct-fetch error, so errors.Is(err,
// ErrScrapeBlocked) still identifies a bot wall after both paths fail.
func FetchRenderedWebsiteContent(ctx context.Context, dfs interfaces.DataForSEO, vals DataForSEOFetchValues, website string, extractWithHtmlTags bool, removeComments bool) (string, error) {
	doc, dfsErr := fetchRenderedHTMLRetrying(ctx, dfs, vals, website)
	if dfsErr == nil {
		return renderDocContent(doc, extractWithHtmlTags, removeComments)
	}
	log.Warn("dataforseo rendered fetch failed, falling back to direct fetch", "url", website, "err", dfsErr)

	content, directErr := FetchWebsiteContent(website, extractWithHtmlTags, removeComments)
	if directErr != nil {
		return "", fmt.Errorf("dataforseo rendered fetch failed (%v); direct fetch failed: %w", dfsErr, directErr)
	}
	return content, nil
}

// FetchRenderedWebsiteDoc is the parsed-tree variant of
// FetchRenderedWebsiteContent: the same DataForSEO-primary / direct-fallback
// fetch, but returning the parsed document BEFORE any node stripping. Callers
// that need pre-strip data (meta og:image tags, <img> attributes — the style
// replication scrape) walk the tree themselves; the same validateScrapedDoc
// gate has already run on both paths.
//
// The second return reports which path produced the document: true = the
// browser-rendered DataForSEO crawl, false = the plain no-JS direct fallback.
// Callers whose analysis assumes rendered HTML (the audit's rendered-parity
// comparison) MUST treat fallback documents as unrendered rather than guess.
// The returned error wraps the direct-fetch error, so errors.Is(err,
// ErrScrapeBlocked) still identifies a bot wall after both paths fail.
func FetchRenderedWebsiteDoc(ctx context.Context, dfs interfaces.DataForSEO, vals DataForSEOFetchValues, website string) (*html.Node, bool, error) {
	doc, dfsErr := fetchRenderedHTMLRetrying(ctx, dfs, vals, website)
	if dfsErr == nil {
		return doc, true, nil
	}
	log.Warn("dataforseo rendered fetch failed, falling back to direct fetch", "url", website, "err", dfsErr)

	doc, directErr := fetchHTML(website)
	if directErr != nil {
		return nil, false, fmt.Errorf("dataforseo rendered fetch failed (%v); direct fetch failed: %w", dfsErr, directErr)
	}
	return doc, false, nil
}

// fetchRenderedHTMLRetrying wraps fetchRenderedHTML with a bounded retry on
// the transient host-busy rejection. Every other failure returns immediately
// (the direct fallback is the right answer for those).
func fetchRenderedHTMLRetrying(ctx context.Context, dfs interfaces.DataForSEO, vals DataForSEOFetchValues, pageURL string) (*html.Node, error) {
	var doc *html.Node
	var err error
	for attempt := 0; ; attempt++ {
		doc, err = fetchRenderedHTML(ctx, dfs, vals, pageURL)
		if err == nil || !errors.Is(err, ErrCrawlHostBusy) || attempt >= crawlHostBusyRetries {
			return doc, err
		}
		log.Warn("dataforseo crawl host busy, retrying", "url", pageURL, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting out busy crawl host: %w", ctx.Err())
		case <-time.After(crawlHostBusyBackoff):
		}
	}
}

// fetchRenderedHTML runs the two-step DataForSEO OnPage flow: a live
// instant_pages crawl that stores the rendered HTML, then a raw_html call to
// collect it. The stored copy has a limited retention window, so collection
// happens immediately. The parsed document goes through the same
// validateScrapedDoc gate as the direct path — a bot wall that serves its
// challenge page with HTTP 200 would otherwise pass every status check here.
func fetchRenderedHTML(ctx context.Context, dfs interfaces.DataForSEO, vals DataForSEOFetchValues, pageURL string) (*html.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, vals.timeout())
	defer cancel()

	// instant_pages requires an absolute URL.
	if !strings.HasPrefix(pageURL, "http://") && !strings.HasPrefix(pageURL, "https://") {
		pageURL = "https://" + pageURL
	}

	task := dto.InstantPagesTask{
		URL:                    pageURL,
		EnableBrowserRendering: true,
		EnableJavascript:       true,
		LoadResources:          true,
		EnableXHR:              true,
		CustomUserAgent:        scrapeUserAgent,
		BrowserPreset:          "desktop",
		AcceptLanguage:         "en-US",
		StoreRawHTML:           true,
		SwitchPool:             true,
		DisableCookiePopup:     true,
	}

	resp, err := dfs.GetInstantPages(ctx, dto.InstantPagesRequest{Tasks: []dto.InstantPagesTask{task}})
	if err != nil {
		return nil, fmt.Errorf("instant_pages request failed: %w", err)
	}
	if len(resp.Tasks) == 0 {
		return nil, fmt.Errorf("instant_pages returned no tasks for %s", pageURL)
	}
	t := resp.Tasks[0]
	if t.StatusCode != vals.taskOK() {
		if strings.Contains(strings.ToLower(t.StatusMessage), "duplicate crawl host") {
			return nil, fmt.Errorf("instant_pages task for %s: %d %s: %w", pageURL, t.StatusCode, t.StatusMessage, ErrCrawlHostBusy)
		}
		return nil, fmt.Errorf("instant_pages task failed for %s: %d %s", pageURL, t.StatusCode, t.StatusMessage)
	}
	if len(t.Result) == 0 || len(t.Result[0].Items) == 0 {
		return nil, fmt.Errorf("instant_pages returned no crawled items for %s", pageURL)
	}

	// The item status is the HTTP status the crawler got from the target site;
	// classify it exactly like the direct path does (challenge/ratelimit codes
	// → blocked, other 4xx/5xx → plain error).
	item := t.Result[0].Items[0]
	switch {
	case item.StatusCode == http.StatusUnauthorized,
		item.StatusCode == http.StatusForbidden,
		item.StatusCode == http.StatusTooManyRequests,
		item.StatusCode == http.StatusServiceUnavailable:
		return nil, fmt.Errorf("%s returned HTTP %d to the rendered crawl: %w", pageURL, item.StatusCode, ErrScrapeBlocked)
	case item.StatusCode >= 400:
		return nil, fmt.Errorf("%s returned HTTP %d to the rendered crawl", pageURL, item.StatusCode)
	}

	rawResp, err := dfs.GetRawHtml(ctx, dto.RawHtmlRequest{Tasks: []dto.RawHtmlTask{{ID: t.ID}}})
	if err != nil {
		return nil, fmt.Errorf("raw_html request failed: %w", err)
	}
	if len(rawResp.Tasks) == 0 {
		return nil, fmt.Errorf("raw_html returned no tasks for crawl %s", t.ID)
	}
	rt := rawResp.Tasks[0]
	if rt.StatusCode != vals.taskOK() {
		return nil, fmt.Errorf("raw_html task failed for crawl %s: %d %s", t.ID, rt.StatusCode, rt.StatusMessage)
	}
	if len(rt.Result) == 0 || len(rt.Result[0].Items) == 0 || rt.Result[0].Items[0].HTML == "" {
		return nil, fmt.Errorf("raw_html returned no stored HTML for crawl %s", t.ID)
	}

	doc, err := html.Parse(io.LimitReader(strings.NewReader(rt.Result[0].Items[0].HTML), maxScrapeBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to parse rendered HTML from %s: %w", pageURL, err)
	}

	if err := validateScrapedDoc(doc); err != nil {
		return nil, fmt.Errorf("%s (rendered): %w", pageURL, err)
	}

	return doc, nil
}
