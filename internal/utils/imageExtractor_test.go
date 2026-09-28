package utils

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseFixture(t *testing.T, fixture string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}

const imageFixture = `<!DOCTYPE html>
<html>
<head>
  <meta property="og:image" content="https://cdn.example.com/hero.jpg">
</head>
<body>
  <header>
    <img src="/assets/logo.png" alt="Company logo">
    <img src="/outside-content.jpg" alt="Not in main">
  </header>
  <main>
    <h1>Post</h1>
    <img src="/images/diagram.png" alt="Architecture diagram">
    <img src="https://cdn.example.com/hero.jpg" alt="Duplicate of og:image">
    <img src="/images/icon-16.png" alt="tiny" width="16" height="16">
    <img src="/images/team-avatar.jpg" alt="Author photo">
    <img src="/images/photo.svg" alt="Vector illustration">
    <img src="data:image/png;base64,AAAA" alt="Inline data">
    <img srcset="/images/small.webp 480w, /images/large.webp 1200w" alt="Responsive shot">
    <img src="/images/close-up.jpg" alt="Product icon in use">
  </main>
</body>
</html>`

func TestExtractContentImages(t *testing.T) {
	doc := parseFixture(t, imageFixture)
	got := ExtractContentImages(doc, "https://example.com/blog/post", 5)

	want := []string{
		"https://cdn.example.com/hero.jpg", // og:image first
		"https://example.com/images/diagram.png",
		"https://example.com/images/large.webp", // srcset resolves to the largest candidate
	}
	if len(got) != len(want) {
		t.Fatalf("got %d images %+v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].URL != want[i] {
			t.Fatalf("image[%d] = %q, want %q", i, got[i].URL, want[i])
		}
	}

	// The header logo and the outside-content image never appear — the walk is
	// scoped to <main>; the avatar (path keyword), tiny icon (declared size),
	// svg (extension), data URI (scheme), icon-alt image (alt keyword) and the
	// og:image duplicate are all filtered.
	for _, img := range got {
		if strings.Contains(img.URL, "logo") || strings.Contains(img.URL, "avatar") ||
			strings.Contains(img.URL, "icon") || strings.Contains(img.URL, "outside") {
			t.Fatalf("filtered image leaked through: %q", img.URL)
		}
	}
}

func TestExtractContentImagesCap(t *testing.T) {
	doc := parseFixture(t, imageFixture)
	got := ExtractContentImages(doc, "https://example.com/blog/post", 2)
	if len(got) != 2 {
		t.Fatalf("cap 2 returned %d images: %+v", len(got), got)
	}
}

func TestExtractContentImagesNoContentNode(t *testing.T) {
	fixture := `<html><body>
	  <div><img src="/pic.jpg" alt="Standalone photo"></div>
	</body></html>`
	doc := parseFixture(t, fixture)
	got := ExtractContentImages(doc, "https://example.com/", 5)
	if len(got) != 1 || got[0].URL != "https://example.com/pic.jpg" {
		t.Fatalf("whole-document fallback failed: %+v", got)
	}
}

func TestExtractArticleBodyText(t *testing.T) {
	fixture := `<html><body>
	  <nav>Site chrome here</nav>
	  <main>
	    <script>var junk = "should never appear";</script>
	    <h1>Real Heading</h1>
	    <p>First paragraph of the article body.</p>
	  </main>
	</body></html>`
	doc := parseFixture(t, fixture)

	text := ExtractArticleBodyText(doc, 0)
	if !strings.Contains(text, "Real Heading") || !strings.Contains(text, "First paragraph") {
		t.Fatalf("body text missing content: %q", text)
	}
	if strings.Contains(text, "junk") || strings.Contains(text, "Site chrome") {
		t.Fatalf("body text leaked script or out-of-content chrome: %q", text)
	}

	capped := ExtractArticleBodyText(doc, 20)
	if len(capped) > 20 {
		t.Fatalf("cap not applied: %d chars %q", len(capped), capped)
	}
	if capped == "" {
		t.Fatalf("cap collapsed the text to empty")
	}
}
