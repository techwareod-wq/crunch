package utils

import (
	"strings"
	"testing"
	"time"
)

func TestCandidateSitemapURLs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "bare host gets https + standard paths",
			in:   "example.com",
			want: []string{
				"https://example.com/sitemap.xml",
				"https://example.com/sitemap_index.xml",
				"https://example.com/post-sitemap.xml",
				"https://example.com/page-sitemap.xml",
			},
		},
		{
			name: "full url keeps scheme, drops path",
			in:   "https://www.example.com/blog/post",
			want: []string{
				"https://www.example.com/sitemap.xml",
				"https://www.example.com/sitemap_index.xml",
				"https://www.example.com/post-sitemap.xml",
				"https://www.example.com/page-sitemap.xml",
			},
		},
		{
			name: "empty yields nil",
			in:   "   ",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CandidateSitemapURLs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("CandidateSitemapURLs(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("CandidateSitemapURLs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseSitemapXML_URLSet(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc></url>
  <url><loc>  https://example.com/b  </loc></url>
  <url><loc></loc></url>
</urlset>`

	entries, children := ParseSitemapXML([]byte(xml))
	if len(children) != 0 {
		t.Fatalf("expected no child sitemaps, got %v", children)
	}
	want := []string{"https://example.com/a", "https://example.com/b"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i].URL != want[i] {
			t.Fatalf("entries[%d].URL = %q, want %q", i, entries[i].URL, want[i])
		}
		if !entries[i].LastMod.IsZero() {
			t.Fatalf("entries[%d].LastMod = %v, want zero for a sitemap without <lastmod>", i, entries[i].LastMod)
		}
	}
}

func TestParseSitemapXML_LastMod(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/date-only</loc><lastmod>2026-08-15</lastmod></url>
  <url><loc>https://example.com/rfc3339</loc><lastmod>2026-08-20T18:23:17+00:00</lastmod></url>
  <url><loc>https://example.com/garbage</loc><lastmod>not a date</lastmod></url>
  <url><loc>https://example.com/absent</loc></url>
</urlset>`

	entries, _ := ParseSitemapXML([]byte(xml))
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %v", entries)
	}

	if want := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC); !entries[0].LastMod.Equal(want) {
		t.Fatalf("date-only lastmod = %v, want %v", entries[0].LastMod, want)
	}
	if want := time.Date(2026, 8, 20, 18, 23, 17, 0, time.UTC); !entries[1].LastMod.Equal(want) {
		t.Fatalf("rfc3339 lastmod = %v, want %v", entries[1].LastMod, want)
	}
	if !entries[2].LastMod.IsZero() {
		t.Fatalf("garbage lastmod should parse to zero, got %v", entries[2].LastMod)
	}
	if !entries[3].LastMod.IsZero() {
		t.Fatalf("absent lastmod should be zero, got %v", entries[3].LastMod)
	}
}

func TestParseSitemapXML_Index(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/post-sitemap.xml</loc></sitemap>
  <sitemap><loc>https://example.com/page-sitemap.xml</loc></sitemap>
</sitemapindex>`

	entries, children := ParseSitemapXML([]byte(xml))
	if len(entries) != 0 {
		t.Fatalf("expected no page URLs from an index, got %v", entries)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 child sitemaps, got %v", children)
	}
}

func TestParseSitemapXML_Malformed(t *testing.T) {
	entries, children := ParseSitemapXML([]byte("not xml at all"))
	if entries != nil || children != nil {
		t.Fatalf("malformed input should yield nils, got entries=%v children=%v", entries, children)
	}
}

func TestCapAndDedupeURLs(t *testing.T) {
	deduped := DedupeURLs([]string{"a", "b", "a", "c", "b"})
	if strings.Join(deduped, ",") != "a,b,c" {
		t.Fatalf("DedupeURLs = %v, want [a b c]", deduped)
	}
	capped := CapURLs([]string{"a", "b", "c", "d"}, 2)
	if strings.Join(capped, ",") != "a,b" {
		t.Fatalf("CapURLs = %v, want [a b]", capped)
	}
	if got := CapURLs([]string{"a"}, 5); len(got) != 1 {
		t.Fatalf("CapURLs under cap = %v, want [a]", got)
	}
}
