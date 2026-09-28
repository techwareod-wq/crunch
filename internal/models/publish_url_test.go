package models

import "testing"

// PublicRemoteURL guards the GSC article join against CMS management links:
// Payload's sidecar reports <base>/admin/collections/<coll>/<id> as remoteUrl
// (2026-08-26 finding — the one published article was keyed on /admin/…).
func TestPublicRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://useindexly.com/admin/collections/posts/20": "",
		"https://cms.example.com/admin":                     "",
		"https://example.com/wp-admin/post.php?post=1":      "",
		"https://example.com/blog/my-post":                  "https://example.com/blog/my-post",
		"https://example.com/administrative-law-guide":      "https://example.com/administrative-law-guide",
		"": "",
	}
	for in, want := range cases {
		if got := PublicRemoteURL(in); got != want {
			t.Errorf("PublicRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}
