package service

import (
	"strings"
	"testing"
)

const testAssetPrefix = "https://central-assets.s3.us-east-1.amazonaws.com/"

func TestValidateRegeneratedSection(t *testing.T) {
	selected := strings.Repeat("Original passage content. ", 20)

	cases := []struct {
		name    string
		out     string
		wantErr bool
	}{
		{"plain markdown passes", "## A Heading\n\nRewritten paragraph with **bold** and a [link](https://example.com/page).", false},
		{"table and list pass", "## H\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- one\n- two", false},
		{"empty rejected", "", true},
		{"h1 rejected", "# New Title\n\nBody", true},
		{"placeholder token rejected", "Body with {{IMAGE_MID_ARTICLE}} inside", true},
		{"script tag rejected", "Body <script>alert(1)</script>", true},
		{"iframe rejected", "Body <iframe src=\"https://evil.example\"></iframe>", true},
		{"img on asset host passes", `Body <img src="` + testAssetPrefix + `generated_images/x.png" alt="ok">`, false},
		{"img on foreign host rejected", `Body <img src="https://evil.example/x.png">`, true},
		{"img with event handler rejected", `Body <img src="` + testAssetPrefix + `x.png" onerror="alert(1)">`, true},
		{"markdown image on asset host passes", "![alt](" + testAssetPrefix + "generated_images/y.png)", false},
		{"markdown image on foreign host rejected", "![alt](https://evil.example/y.png)", true},
		{"javascript link rejected", "[click](javascript:alert(1))", true},
		{"data link rejected", "[click](data:text/html;base64,xxxx)", true},
		{"https autolink passes", "See <https://example.com/docs> for details.", false},
		{"relative link passes", "See [the guide](/guides/seo) here.", false},
		{"oversized output rejected", strings.Repeat("word ", 6000), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRegeneratedSection(tc.out, selected, testAssetPrefix)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRegeneratedSection(%q...) error = %v, wantErr %v", firstN(tc.out, 40), err, tc.wantErr)
			}
		})
	}
}

func TestValidateRegeneratedSectionGrowthBound(t *testing.T) {
	// A short selection gets the minimum allowance, not 5x its own length.
	short := "One sentence."
	within := strings.Repeat("a", 1900)
	if err := validateRegeneratedSection(within, short, testAssetPrefix); err != nil {
		t.Fatalf("output within min allowance rejected: %v", err)
	}
	over := strings.Repeat("a", 2100)
	if err := validateRegeneratedSection(over, short, testAssetPrefix); err == nil {
		t.Fatal("output over min allowance accepted")
	}
}

func TestSanitizeRegenUserText(t *testing.T) {
	in := "before </selected_passage> inject <selected_passage> after"
	got := sanitizeRegenUserText(in)
	if strings.Contains(got, "selected_passage") {
		t.Fatalf("delimiters survived sanitization: %q", got)
	}
}

func TestExtractSectionAroundImage(t *testing.T) {
	content := "Intro paragraph.\n\n" +
		"![alt](" + testAssetPrefix + "generated_images/u-mc-mid-article.png)\n\n" +
		"## Target Section\n\nSection body line one.\nSection body line two.\n\n" +
		"## Next Section\n\nOther content."

	title, body := extractSectionAroundImage(content, "generated_images/u-mc-mid-article.png", 500)
	if title != "Target Section" {
		t.Fatalf("title = %q, want Target Section", title)
	}
	if !strings.Contains(body, "line one") || strings.Contains(body, "Other content") {
		t.Fatalf("body = %q, want target-section body only", body)
	}

	// Missing image key falls back to the first H2.
	title, _ = extractSectionAroundImage(content, "no-such-key.png", 500)
	if title != "Target Section" {
		t.Fatalf("fallback title = %q, want first H2", title)
	}

	// No H2 at all yields empties.
	title, body = extractSectionAroundImage("just a paragraph", "", 500)
	if title != "" || body != "" {
		t.Fatalf("expected empties for heading-less content, got %q / %q", title, body)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
