package service

import (
	"errors"
	"fmt"
	"testing"

	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/utils"
)

func TestIsArticleLikeURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		set  map[string]bool
		want bool
	}{
		{name: "root fails", url: "https://example.com/", want: false},
		{name: "blog marker passes", url: "https://example.com/blog/how-to-x", want: true},
		{name: "posts marker passes", url: "https://example.com/posts/x", want: true},
		{name: "date segment passes", url: "https://example.com/2026/08/launch-recap", want: true},
		{name: "depth 2 passes", url: "https://example.com/guides-x/deep-dive", want: true},
		{name: "depth 1 without marker fails", url: "https://example.com/some-page", want: false},
		{name: "pricing excluded", url: "https://example.com/pricing", want: false},
		{name: "about excluded even at depth", url: "https://example.com/company/about-us", want: false},
		{name: "category listing excluded", url: "https://example.com/category/seo", want: false},
		{name: "sitemap membership rescues depth-1", url: "https://example.com/some-post",
			set: map[string]bool{utils.NormalizeGSCPageURL("https://example.com/some-post"): true}, want: true},
		{name: "excluded page not rescued by sitemap", url: "https://example.com/pricing",
			set: map[string]bool{utils.NormalizeGSCPageURL("https://example.com/pricing"): true}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isArticleLikeURL(tc.url, tc.set); got != tc.want {
				t.Fatalf("isArticleLikeURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestValidateSourceURLs(t *testing.T) {
	got, err := validateSourceURLs([]string{
		" https://example.com/blog/a ",
		"https://example.com/blog/a", // dup collapses
		"https://competitor.io/posts/b",
	}, 1, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 deduped URLs", got)
	}

	if _, err := validateSourceURLs([]string{"not-a-url"}, 1, 10); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("relative URL should be invalid, got %v", err)
	}
	if _, err := validateSourceURLs([]string{"ftp://example.com/x"}, 1, 10); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("non-http scheme should be invalid, got %v", err)
	}
	if _, err := validateSourceURLs(nil, 1, 10); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("empty list should be invalid, got %v", err)
	}
	if _, err := validateSourceURLs([]string{"https://a.com/1", "https://a.com/2", "https://a.com/3"}, 1, 2); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("over-cap list should be invalid, got %v", err)
	}
	if _, err := validateSourceURLs([]string{"https://a.com/1"}, 2, 10); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("under-min list should be invalid, got %v", err)
	}
}

func TestValidateUploadedRefs(t *testing.T) {
	prefix := styleUploadKeyPrefix("user1", "run1")
	thumb := func(key string) sr.StyleUploadedImageRef {
		return sr.StyleUploadedImageRef{Key: key, Position: "thumbnail"}
	}
	mid := func(key string) sr.StyleUploadedImageRef {
		return sr.StyleUploadedImageRef{Key: key, Position: "mid-article"}
	}

	got, err := validateUploadedRefs([]sr.StyleUploadedImageRef{
		thumb(prefix + "-thumbnail-100.png"),
		thumb(prefix + "-thumbnail-100.png"), // dup collapses
		mid("  " + prefix + "-mid-article-200.jpg  "),
	}, prefix, 12)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Key != prefix+"-thumbnail-100.png" || got[1].Key != prefix+"-mid-article-200.jpg" {
		t.Fatalf("got %v, want 2 deduped trimmed refs", got)
	}
	if got[0].Position != "thumbnail" || got[1].Position != "mid-article" {
		t.Fatalf("positions lost through validation: %v", got)
	}

	// A key outside this run's namespace could point synthesis at an
	// arbitrary bucket object — hard reject.
	foreign := styleUploadKeyPrefix("user1", "OTHER") + "-thumbnail-1.png"
	if _, err := validateUploadedRefs([]sr.StyleUploadedImageRef{thumb(foreign)}, prefix, 12); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("foreign-namespace key should be invalid, got %v", err)
	}
	if _, err := validateUploadedRefs([]sr.StyleUploadedImageRef{thumb("generated_images/whatever.png")}, prefix, 12); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("out-of-namespace key should be invalid, got %v", err)
	}

	// Unknown position buckets are rejected.
	bad := sr.StyleUploadedImageRef{Key: prefix + "-x-1.png", Position: "banner"}
	if _, err := validateUploadedRefs([]sr.StyleUploadedImageRef{bad}, prefix, 12); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("unknown position should be invalid, got %v", err)
	}

	// The cap is PER POSITION — 12 thumbs + 12 mids is fine, 13 thumbs isn't.
	many := make([]sr.StyleUploadedImageRef, 0, 25)
	for i := 0; i < 12; i++ {
		many = append(many, thumb(fmt.Sprintf("%s-thumbnail-%d.png", prefix, i)))
		many = append(many, mid(fmt.Sprintf("%s-mid-article-%d.png", prefix, i)))
	}
	if _, err := validateUploadedRefs(many, prefix, 12); err != nil {
		t.Fatalf("12 per bucket should be valid, got %v", err)
	}
	many = append(many, thumb(prefix+"-thumbnail-extra.png"))
	if _, err := validateUploadedRefs(many, prefix, 12); !errors.Is(err, sr.ErrStyleRunInvalidInput) {
		t.Fatalf("13 thumbs should be over the per-bucket cap, got %v", err)
	}

	// Empty list is fine — uploads are optional.
	if got, err := validateUploadedRefs(nil, prefix, 12); err != nil || len(got) != 0 {
		t.Fatalf("nil uploads should validate to empty, got %v / %v", got, err)
	}
}
