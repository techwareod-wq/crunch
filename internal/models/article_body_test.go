package models

import "testing"

func TestStripArticleTitleAndThumbnail(t *testing.T) {
	tests := []struct {
		name      string
		md        string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "pipeline placeholder then h1",
			md:        "{{IMAGE_THUMBNAIL}}\n# How to Bake Bread\n\nMix flour and water.\n\n{{IMAGE_MID_ARTICLE}}\n\n## Step",
			wantTitle: "How to Bake Bread",
			wantBody:  "Mix flour and water.\n\n{{IMAGE_MID_ARTICLE}}\n\n## Step",
		},
		{
			name:      "markdown thumbnail then h1",
			md:        "![loaf](https://cdn/thumb.png)\n\n# How to Bake Bread\n\nIntro paragraph.",
			wantTitle: "How to Bake Bread",
			wantBody:  "Intro paragraph.",
		},
		{
			name:      "editor round-trip <img> then h1",
			md:        "<img src=\"https://cdn/thumb.png\" alt=\"loaf\" />\n\n# Title\n\nBody text.",
			wantTitle: "Title",
			wantBody:  "Body text.",
		},
		{
			name:      "img wrapped in a lone paragraph",
			md:        "<p><img src=\"https://cdn/thumb.png\" alt=\"loaf\"></p>\n\n# Title\n\nBody.",
			wantTitle: "Title",
			wantBody:  "Body.",
		},
		{
			name:      "title before thumbnail still handled",
			md:        "# Title\n\n![loaf](https://cdn/thumb.png)\n\nBody.",
			wantTitle: "Title",
			wantBody:  "Body.",
		},
		{
			name:      "no thumbnail, only title",
			md:        "# Title\n\nBody paragraph.",
			wantTitle: "Title",
			wantBody:  "Body paragraph.",
		},
		{
			name:      "no leading title or thumbnail leaves body intact",
			md:        "Just a body paragraph.\n\n## H2",
			wantTitle: "",
			wantBody:  "Just a body paragraph.\n\n## H2",
		},
		{
			name:      "in-body mid-article image is not stripped",
			md:        "# Title\n\nIntro.\n\n![mid](https://cdn/mid.png)\n\n## Next",
			wantTitle: "Title",
			wantBody:  "Intro.\n\n![mid](https://cdn/mid.png)\n\n## Next",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTitle, gotBody := StripArticleTitleAndThumbnail(tt.md)
			if gotTitle != tt.wantTitle {
				t.Fatalf("title = %q, want %q", gotTitle, tt.wantTitle)
			}
			if gotBody != tt.wantBody {
				t.Fatalf("body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

func TestComposeAndStripRoundTrip(t *testing.T) {
	title := "How to Bake Bread"
	thumbURL := "https://cdn.example/thumb.png"
	thumbAlt := "a golden loaf"
	body := "Intro paragraph.\n\n![mid](https://cdn.example/mid.png)\n\n## Step one\n\nMore body."

	merged := ComposeArticleWithTitleAndThumbnail(title, thumbURL, thumbAlt, body)

	gotTitle, gotBody := StripArticleTitleAndThumbnail(merged)
	if gotTitle != title {
		t.Fatalf("round-trip title = %q, want %q", gotTitle, title)
	}
	if gotBody != body {
		t.Fatalf("round-trip body = %q, want %q", gotBody, body)
	}
}

func TestComposeOmitsEmptyParts(t *testing.T) {
	// No thumbnail, no title: body passes through unchanged.
	if got := ComposeArticleWithTitleAndThumbnail("", "", "", "Body only."); got != "Body only." {
		t.Fatalf("compose with empty parts = %q", got)
	}
	// Title but no thumbnail.
	if got := ComposeArticleWithTitleAndThumbnail("T", "", "", "Body."); got != "# T\n\nBody." {
		t.Fatalf("compose title-only = %q", got)
	}
}
