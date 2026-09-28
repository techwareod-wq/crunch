package service

import "testing"

// testSlugMaxLen mirrors values.contentGeneration.slugMaxLen. The expectations
// below are computed against this cap, so it is pinned here rather than read
// from config.
const testSlugMaxLen = 60

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already clean", "seo-blog-slug", "seo-blog-slug"},
		{"lowercases and hyphenates spaces", "SEO Blog Slug", "seo-blog-slug"},
		{"strips stop words", "the best guide for a marketer", "best-guide-marketer"},
		{"collapses punctuation and underscores", "seo__blog!!slug", "seo-blog-slug"},
		{"trims leading/trailing separators", "  -seo blog-  ", "seo-blog"},
		{"all stop words falls back to words", "the a for", "the-a-for"},
		{"empty stays empty", "   ", ""},
		{
			name: "caps length on a word boundary",
			in:   "how to choose the best content marketing automation platform quickly",
			// stop words (to, the) dropped, then trimmed to <=60 chars on a hyphen
			want: "how-choose-best-content-marketing-automation-platform",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSlug(tt.in, testSlugMaxLen)
			if got != tt.want {
				t.Fatalf("normalizeSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(got) > testSlugMaxLen {
				t.Fatalf("normalizeSlug(%q) length %d exceeds cap %d", tt.in, len(got), testSlugMaxLen)
			}
		})
	}
}

func TestExtractOutlineSlug(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "picks and normalizes url_slug",
			json: `{"h1":"x","url_slug":"The Best Guide For Marketers"}`,
			want: "best-guide-marketers",
		},
		{"missing slug", `{"h1":"x"}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractOutlineSlug(tt.json, testSlugMaxLen); got != tt.want {
				t.Fatalf("extractOutlineSlug(%q) = %q, want %q", tt.json, got, tt.want)
			}
		})
	}
}
