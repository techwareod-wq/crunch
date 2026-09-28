package utils

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// normalPageHTML is a realistic homepage whose visible text comfortably
// clears minScrapeTextChars.
const normalPageHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Acme Analytics</title>
	<meta name="description" content="Acme Analytics turns raw product events into revenue insights.">
</head>
<body>
	<h1>Acme Analytics</h1>
	<h2>Understand your users</h2>
	<p>Acme Analytics is the product analytics platform for modern SaaS teams.
	Track activation, retention, and revenue in one place without writing SQL.
	Our warehouse-native pipeline syncs with your existing stack in minutes and
	keeps your metrics consistent across every dashboard your team relies on.</p>
	<h2>Built for growth teams</h2>
	<p>From funnel analysis to cohort retention curves, Acme gives product
	managers and growth engineers the answers they need to ship features that
	move the numbers that matter.</p>
</body>
</html>`

// challengePageHTML mimics a Cloudflare-style challenge served with HTTP 200.
// Padded past minScrapeTextChars so the test exercises the marker check, not
// the length check.
var challengePageHTML = `<!DOCTYPE html>
<html>
<head><title>Just a moment...</title></head>
<body>
	<h1>www.example.com</h1>
	<p>Please enable JavaScript and cookies to continue.</p>
	<p>` + strings.Repeat("Verifying your connection before proceeding. ", 10) + `</p>
</body>
</html>`

func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func serveHTML(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	})
}

func TestFetchWebsiteContent_NormalPage(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	content, err := FetchWebsiteContent(srv.URL, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "product analytics platform") {
		t.Errorf("content missing expected text, got: %q", Truncate(content, 200))
	}
}

func TestFetchWebsiteContent_ChallengePage(t *testing.T) {
	srv := serveHTML(t, challengePageHTML)

	_, err := FetchWebsiteContent(srv.URL, true, true)
	if !errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("want ErrScrapeBlocked, got: %v", err)
	}
}

func TestFetchWebsiteContent_BlockedStatusCodes(t *testing.T) {
	for _, code := range []int{401, 403, 429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, normalPageHTML)
			})

			_, err := FetchWebsiteContent(srv.URL, true, true)
			if !errors.Is(err, ErrScrapeBlocked) {
				t.Fatalf("want ErrScrapeBlocked for HTTP %d, got: %v", code, err)
			}
		})
	}
}

func TestFetchWebsiteContent_NotFoundIsPlainError(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	_, err := FetchWebsiteContent(srv.URL, true, true)
	if err == nil {
		t.Fatal("want error for HTTP 404, got nil")
	}
	if errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("404 must be a plain error, not ErrScrapeBlocked: %v", err)
	}
}

func TestFetchWebsiteContent_NearEmptyPage(t *testing.T) {
	srv := serveHTML(t, `<html><body><p>Coming soon</p></body></html>`)

	_, err := FetchWebsiteContent(srv.URL, true, true)
	if !errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("want ErrScrapeBlocked for near-empty page, got: %v", err)
	}
}

func TestFetchWebsiteContent_ScriptOnlyShellIsEmpty(t *testing.T) {
	// A CSR shell: no visible text, but a large JS payload that must not
	// count toward the minimum-text check.
	page := `<html><head><script>` + strings.Repeat("var x = 'not visible content'; ", 50) +
		`</script></head><body><div id="root"></div></body></html>`
	srv := serveHTML(t, page)

	_, err := FetchWebsiteContent(srv.URL, true, true)
	if !errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("want ErrScrapeBlocked for script-only shell, got: %v", err)
	}
}

func TestFetchHTML_SendsBrowserHeaders(t *testing.T) {
	var gotUA, gotAccept, gotLang string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotLang = r.Header.Get("Accept-Language")
		fmt.Fprint(w, normalPageHTML)
	})

	if _, err := FetchWebsiteContent(srv.URL, true, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotUA, "Chrome/") {
		t.Errorf("User-Agent not Chrome-like: %q", gotUA)
	}
	if !strings.Contains(gotAccept, "text/html") {
		t.Errorf("Accept header missing text/html: %q", gotAccept)
	}
	if !strings.Contains(gotLang, "en-US") {
		t.Errorf("Accept-Language missing en-US: %q", gotLang)
	}
}

func TestExtractArticleStructure_NormalPage(t *testing.T) {
	srv := serveHTML(t, normalPageHTML)

	result, err := ExtractArticleStructure(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.H1 != "Acme Analytics" {
		t.Errorf("H1 = %q, want %q", result.H1, "Acme Analytics")
	}
	if len(result.H2s) != 2 {
		t.Errorf("H2s = %v, want 2 entries", result.H2s)
	}
	if result.MetaDescription == "" {
		t.Error("MetaDescription empty, want the page's description")
	}
	if result.WordCount == 0 {
		t.Error("WordCount = 0, want > 0")
	}
}

func TestExtractArticleStructure_ChallengePage(t *testing.T) {
	srv := serveHTML(t, challengePageHTML)

	_, err := ExtractArticleStructure(srv.URL)
	if !errors.Is(err, ErrScrapeBlocked) {
		t.Fatalf("want ErrScrapeBlocked, got: %v", err)
	}
}

func TestFetchHTML_ReplaysCookieAcrossRedirect(t *testing.T) {
	var sawCookie bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "wall", Value: "cleared", Path: "/"})
		http.Redirect(w, r, "/real", http.StatusFound)
	})
	mux.HandleFunc("/real", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("wall"); err == nil && c.Value == "cleared" {
			sawCookie = true
		}
		fmt.Fprint(w, normalPageHTML)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, err := FetchWebsiteContent(srv.URL, true, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawCookie {
		t.Error("cookie set before redirect was not replayed on the redirected request")
	}
}
