package utils

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ExtractedImage is one candidate content image found on a scraped page.
// OG marks the page's og:image — the hero/thumbnail representative — so
// callers can bucket it separately from in-article imagery.
type ExtractedImage struct {
	URL string
	Alt string
	OG  bool
}

// minContentImageDim is the smallest width/height attribute (px) an <img> can
// declare and still count as content. Icons, avatars and tracking pixels
// declare tiny dimensions; real article imagery is comfortably larger. Images
// with no size attributes pass — most CMSs omit them on content images.
const minContentImageDim = 100

// nonContentImageMarkers are case-insensitive substrings of a URL path or alt
// text that mark an image as chrome rather than content (logos, icons,
// avatars, tracking sprites).
var nonContentImageMarkers = []string{
	"logo", "icon", "avatar", "favicon", "sprite", "badge", "emoji", "gravatar",
}

// skippedImageExtensions are formats that are almost never article content:
// vector logos/icons and animated decorations.
var skippedImageExtensions = []string{".svg", ".gif", ".ico"}

// ExtractContentImages walks an already-parsed HTML tree and returns up to
// maxImages candidate content images: the og:image (when it passes the
// filters) followed by <img> elements within the content node
// (FindContentNode, whole document fallback). Relative URLs are resolved
// against pageURL; duplicates, icons/logos/avatars (by extension, URL/alt
// keyword, or tiny declared dimensions) and data: URIs are dropped.
func ExtractContentImages(doc *html.Node, pageURL string, maxImages int) []ExtractedImage {
	if doc == nil || maxImages <= 0 {
		return nil
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		base = nil
	}

	seen := map[string]struct{}{}
	var out []ExtractedImage
	add := func(rawURL, alt string, og bool) {
		if len(out) >= maxImages {
			return
		}
		resolved, ok := resolveImageURL(base, rawURL)
		if !ok || !isContentImage(resolved, alt) {
			return
		}
		if _, dup := seen[resolved]; dup {
			return
		}
		seen[resolved] = struct{}{}
		out = append(out, ExtractedImage{URL: resolved, Alt: alt, OG: og})
	}

	if og := extractOGImage(doc); og != "" {
		add(og, "", true)
	}

	content := FindContentNode(doc)
	if content == nil {
		content = doc
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxImages {
			return
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			src, srcset, alt, width, height := imgAttrs(n)
			if !tinyDeclaredSize(width, height) {
				if src == "" {
					src = firstSrcsetURL(srcset)
				}
				if src != "" {
					add(src, alt, false)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(content)

	return out
}

// extractOGImage returns the first og:image meta content in the document
// ("" when absent). Matched on property or name — some sites use either.
func extractOGImage(n *html.Node) string {
	var walk func(*html.Node) (string, bool)
	walk = func(n *html.Node) (string, bool) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			var key, content string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "property", "name":
					key = strings.ToLower(strings.TrimSpace(a.Val))
				case "content":
					content = strings.TrimSpace(a.Val)
				}
			}
			if key == "og:image" && content != "" {
				return content, true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v, ok := walk(c); ok {
				return v, true
			}
		}
		return "", false
	}
	og, _ := walk(n)
	return og
}

func imgAttrs(n *html.Node) (src, srcset, alt, width, height string) {
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "src":
			src = strings.TrimSpace(a.Val)
		case "srcset":
			srcset = strings.TrimSpace(a.Val)
		case "alt":
			alt = strings.TrimSpace(a.Val)
		case "width":
			width = strings.TrimSpace(a.Val)
		case "height":
			height = strings.TrimSpace(a.Val)
		}
	}
	return
}

// firstSrcsetURL picks the last candidate of a srcset — candidates are
// conventionally listed smallest-first, so the last is the largest variant.
func firstSrcsetURL(srcset string) string {
	if srcset == "" {
		return ""
	}
	candidates := strings.Split(srcset, ",")
	for i := len(candidates) - 1; i >= 0; i-- {
		fields := strings.Fields(strings.TrimSpace(candidates[i]))
		if len(fields) > 0 && fields[0] != "" {
			return fields[0]
		}
	}
	return ""
}

// tinyDeclaredSize reports whether the width/height attributes, when present
// and parseable, declare an icon-sized image. Absent/unparseable attributes
// don't disqualify.
func tinyDeclaredSize(width, height string) bool {
	for _, v := range []string{width, height} {
		v = strings.TrimSuffix(strings.TrimSpace(v), "px")
		if v == "" {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n < minContentImageDim {
			return true
		}
	}
	return false
}

// resolveImageURL resolves a possibly-relative image URL against the page URL
// and rejects non-HTTP schemes (data:, blob:).
func resolveImageURL(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if base != nil {
		ref = base.ResolveReference(ref)
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return "", false
	}
	if ref.Host == "" {
		return "", false
	}
	return ref.String(), true
}

// isContentImage applies the extension and keyword filters to a resolved URL
// and its alt text.
func isContentImage(resolvedURL, alt string) bool {
	u, err := url.Parse(resolvedURL)
	if err != nil {
		return false
	}
	lowerPath := strings.ToLower(u.Path)
	for _, ext := range skippedImageExtensions {
		if strings.HasSuffix(lowerPath, ext) {
			return false
		}
	}
	lowerAlt := strings.ToLower(alt)
	for _, marker := range nonContentImageMarkers {
		if strings.Contains(lowerPath, marker) || strings.Contains(lowerAlt, marker) {
			return false
		}
	}
	return true
}
