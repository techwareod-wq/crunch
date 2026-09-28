package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreservesImagePlaceholders(t *testing.T) {
	original := strings.Join([]string{
		"{{IMAGE_THUMBNAIL}}",
		"# Title",
		"Intro paragraph.",
		"{{IMAGE_MID_ARTICLE}}",
		"## Section",
		"Body.",
	}, "\n")

	// A link-only edit that keeps both placeholders.
	linked := strings.Replace(original, "Body.", "Body with a [link](https://example.com/x).", 1)
	if !preservesImagePlaceholders(original, linked) {
		t.Fatalf("expected placeholders preserved for a link-only edit")
	}

	// A response that dropped the mid-article placeholder must be rejected.
	dropped := strings.Replace(original, "{{IMAGE_MID_ARTICLE}}\n", "", 1)
	if preservesImagePlaceholders(original, dropped) {
		t.Fatalf("expected a dropped placeholder to fail preservation")
	}

	// A response that duplicated the thumbnail placeholder must also be rejected.
	duplicated := original + "\n{{IMAGE_THUMBNAIL}}"
	if preservesImagePlaceholders(original, duplicated) {
		t.Fatalf("expected a duplicated placeholder to fail preservation")
	}
}

func TestParseInternalLinkInsertionResponse(t *testing.T) {
	// Valid response: article markdown is extracted.
	valid := `{"article_markdown":"# Title\n\nBody with [a link](https://example.com/a).","links_added":[{"url":"https://example.com/a","anchor_text":"a link","reason":"relevant"}]}`
	var ok internalLinkInsertionResponse
	if err := json.Unmarshal([]byte(valid), &ok); err != nil {
		t.Fatalf("valid JSON failed to parse: %v", err)
	}
	if !strings.Contains(ok.ArticleMarkdown, "[a link]") {
		t.Fatalf("article markdown not extracted: %q", ok.ArticleMarkdown)
	}
	if len(ok.LinksAdded) != 1 || ok.LinksAdded[0].URL != "https://example.com/a" {
		t.Fatalf("links_added not parsed: %+v", ok.LinksAdded)
	}

	// Invalid JSON: parsing fails, so the handler keeps the original article.
	var bad internalLinkInsertionResponse
	if err := json.Unmarshal([]byte("not json"), &bad); err == nil {
		t.Fatalf("expected invalid JSON to fail parsing")
	}
}
