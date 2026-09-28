package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
)

func instantPagesOK(taskID string, itemStatus int) *dto.InstantPagesResponse {
	return &dto.InstantPagesResponse{
		Tasks: []dto.InstantPagesRespTask{{
			ID:         taskID,
			StatusCode: defaultDataForSEOTaskOK,
			Result: []dto.InstantPagesResult{{
				Items: []dto.InstantPageItem{{StatusCode: itemStatus}},
			}},
		}},
	}
}

func rawHtmlOK(htmlBody string) *dto.RawHtmlResponse {
	return &dto.RawHtmlResponse{
		Tasks: []dto.RawHtmlRespTask{{
			StatusCode: defaultDataForSEOTaskOK,
			Result: []dto.RawHtmlResult{{
				Items: []dto.RawHtmlItem{{HTML: htmlBody}},
			}},
		}},
	}
}

// TestDataForSEOFetchValues_Defaults pins the zero-value fallbacks: an absent
// apis.dataForSEO block in values.yaml must not disable the primary fetch path.
func TestDataForSEOFetchValues_Defaults(t *testing.T) {
	var zero DataForSEOFetchValues
	if got := zero.taskOK(); got != defaultDataForSEOTaskOK {
		t.Fatalf("zero taskOK() = %d, want %d", got, defaultDataForSEOTaskOK)
	}
	if got := zero.timeout(); got != defaultDataForSEOFetchTimeout {
		t.Fatalf("zero timeout() = %v, want %v", got, defaultDataForSEOFetchTimeout)
	}

	set := DataForSEOFetchValues{TaskOKStatusCode: 12345, FetchTimeout: 10 * time.Second}
	if got := set.taskOK(); got != 12345 {
		t.Fatalf("set taskOK() = %d, want 12345", got)
	}
	if got := set.timeout(); got != 10*time.Second {
		t.Fatalf("set timeout() = %v, want 10s", got)
	}
}

func TestFetchRenderedWebsiteContent_PrimaryPathSkipsDirectFetch(t *testing.T) {
	directHit := false
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		directHit = true
		fmt.Fprint(w, normalPageHTML)
	})

	dfs := &fakeDataForSEO{
		instantResp: instantPagesOK("task-1", http.StatusOK),
		rawResp:     rawHtmlOK(normalPageHTML),
	}

	content, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "product analytics platform") {
		t.Fatalf("content missing expected text, got: %q", Truncate(content, 200))
	}
	if directHit {
		t.Fatal("direct fetch ran even though the DataForSEO path succeeded")
	}
}

func TestFetchRenderedWebsiteContent_FallsBackOnAPIError(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	dfs := &fakeDataForSEO{instantErr: errors.New("dataforseo down")}

	content, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "product analytics platform") {
		t.Fatalf("fallback content missing expected text, got: %q", Truncate(content, 200))
	}
}

func TestFetchRenderedWebsiteContent_FallsBackOnStoredChallengePage(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	// The crawl "succeeded" (task OK, HTTP 200) but what got stored is a bot
	// wall's challenge page — validateScrapedDoc must reject it and the direct
	// fetch must take over.
	dfs := &fakeDataForSEO{
		instantResp: instantPagesOK("task-1", http.StatusOK),
		rawResp:     rawHtmlOK(challengePageHTML),
	}

	content, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "product analytics platform") {
		t.Fatalf("fallback content missing expected text, got: %q", Truncate(content, 200))
	}
}

func TestFetchRenderedWebsiteContent_BothBlockedSurfacesErrScrapeBlocked(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	dfs := &fakeDataForSEO{
		instantResp: instantPagesOK("task-1", http.StatusForbidden),
	}

	_, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true)
	if err == nil {
		t.Fatal("expected an error when both paths are blocked")
	}
	if !errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("expected ErrScrapeBlocked, got: %v", err)
	}
}

// TestRawHtmlResult_ItemsBothShapes pins the dual-shape items decoding: the
// raw_html docs show items as an object while live responses have been seen as
// an array; both must yield the HTML.
func TestRawHtmlResult_ItemsBothShapes(t *testing.T) {
	for name, payload := range map[string]string{
		"array":  `{"items": [{"html": "<html>a</html>"}]}`,
		"object": `{"items": {"html": "<html>a</html>"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var res dto.RawHtmlResult
			if err := json.Unmarshal([]byte(payload), &res); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if len(res.Items) != 1 || res.Items[0].HTML != "<html>a</html>" {
				t.Fatalf("unexpected items: %+v", res.Items)
			}
		})
	}
}

func TestFetchRenderedWebsiteContent_FailedTaskFallsBack(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	dfs := &fakeDataForSEO{
		instantResp: &dto.InstantPagesResponse{
			Tasks: []dto.InstantPagesRespTask{{
				ID:            "task-1",
				StatusCode:    40501,
				StatusMessage: "Invalid Field",
			}},
		},
	}

	content, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "product analytics platform") {
		t.Fatalf("fallback content missing expected text, got: %q", Truncate(content, 200))
	}
}

// hostBusyResp is the 40501 "duplicate crawl host" rejection DataForSEO
// returns while another crawl holds the host lock.
func hostBusyResp() *dto.InstantPagesResponse {
	return &dto.InstantPagesResponse{
		Tasks: []dto.InstantPagesRespTask{{
			ID:            "task-busy",
			StatusCode:    40501,
			StatusMessage: "Invalid Field: 'url' - duplicate crawl host.",
		}},
	}
}

// TestFetchRenderedWebsiteDoc_RetriesOnBusyHost pins the transient-lock
// recovery: busy → wait → success, without ever touching the direct fallback.
func TestFetchRenderedWebsiteDoc_RetriesOnBusyHost(t *testing.T) {
	oldBackoff := crawlHostBusyBackoff
	crawlHostBusyBackoff = time.Millisecond
	defer func() { crawlHostBusyBackoff = oldBackoff }()

	directHit := false
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		directHit = true
		fmt.Fprint(w, normalPageHTML)
	})

	dfs := &fakeDataForSEO{
		instantSeq: []*dto.InstantPagesResponse{
			hostBusyResp(),
			instantPagesOK("task-2", http.StatusOK),
		},
		rawResp: rawHtmlOK(normalPageHTML),
	}

	doc, rendered, err := FetchRenderedWebsiteDoc(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc == nil || !rendered {
		t.Fatalf("doc=%v rendered=%v, want rendered doc after retry", doc != nil, rendered)
	}
	if dfs.instantCalls != 2 {
		t.Fatalf("instant calls = %d, want 2 (one busy, one retry)", dfs.instantCalls)
	}
	if directHit {
		t.Fatal("direct fetch ran even though the retry succeeded")
	}
}

// TestFetchRenderedWebsiteDoc_BusyExhaustionFallsBackUnrendered pins the
// honesty contract: retries exhausted → direct fallback serves the doc and
// rendered=false so rendered-dependent callers can skip it.
func TestFetchRenderedWebsiteDoc_BusyExhaustionFallsBackUnrendered(t *testing.T) {
	oldBackoff := crawlHostBusyBackoff
	crawlHostBusyBackoff = time.Millisecond
	defer func() { crawlHostBusyBackoff = oldBackoff }()

	srv := serveHTML(t, normalPageHTML)

	dfs := &fakeDataForSEO{instantSeq: []*dto.InstantPagesResponse{hostBusyResp()}}

	doc, rendered, err := FetchRenderedWebsiteDoc(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc == nil || rendered {
		t.Fatalf("doc=%v rendered=%v, want unrendered fallback doc", doc != nil, rendered)
	}
	if want := 1 + crawlHostBusyRetries; dfs.instantCalls != want {
		t.Fatalf("instant calls = %d, want %d (initial + bounded retries)", dfs.instantCalls, want)
	}
}

// TestFetchRenderedWebsiteContent_NonBusyTaskFailureDoesNotRetry pins that
// ordinary task failures (40501 without the busy message included) skip the
// retry loop entirely — the fallback is the right answer for those.
func TestFetchRenderedWebsiteContent_NonBusyTaskFailureDoesNotRetry(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	dfs := &fakeDataForSEO{
		instantSeq: []*dto.InstantPagesResponse{{
			Tasks: []dto.InstantPagesRespTask{{ID: "task-1", StatusCode: 40501, StatusMessage: "Invalid Field"}},
		}},
	}

	if _, err := FetchRenderedWebsiteContent(context.Background(), dfs, DataForSEOFetchValues{}, srv.URL, false, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dfs.instantCalls != 1 {
		t.Fatalf("instant calls = %d, want 1 (no retry on non-busy failure)", dfs.instantCalls)
	}
}
