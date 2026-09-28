package models

import (
	"regexp"
	"strings"
)

// The stored article body carries neither its title (an H1) nor its thumbnail
// image. Both live in dedicated fields — the title on the ScheduledArticle doc
// (and ProposedTitle), the thumbnail as the thumbnail-position CGEImage — and
// are re-composed per consumer:
//   - the read path sends the title and thumbnail as their own fields alongside
//     the body; the review UI composes them back onto the body client-side for
//     display, so the body it can copy stays free of both, and
//   - the CMS publish path maps them onto the platform's own title / thumbnail
//     fields, leaving the published body free of both.
//
// StripArticleTitleAndThumbnail (applied to the edited document the review UI
// posts back) and ComposeArticleWithTitleAndThumbnail (the canonical merge the
// client mirrors) are the two ends of that transform and must stay
// inverse-consistent.

// htmlTagRE matches a single HTML tag, used to test whether a paragraph that
// wraps an <img> holds anything other than the image.
var htmlTagRE = regexp.MustCompile(`<[^>]*>`)

// StripArticleTitleAndThumbnail removes a leading thumbnail image and a leading
// H1 title from article markdown, returning the extracted title text (empty when
// there was no leading H1) and the remaining body. Only content appearing before
// the first real paragraph or heading is considered, so the mid-article image
// and any in-body headings are left untouched. The two leading elements may
// appear in either order. It recognises the thumbnail in every form the body can
// hold it — the pipeline's {{IMAGE_THUMBNAIL}} placeholder, a markdown image
// (![alt](url)), and the raw <img> tag the editor round-trips — so it is safe to
// run on both pipeline output and user edits.
func StripArticleTitleAndThumbnail(md string) (title, body string) {
	lines := strings.Split(md, "\n")
	i := 0
	thumbStripped := false
	titleStripped := false
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			i++
			continue
		}
		if !thumbStripped && isThumbnailLine(trimmed) {
			thumbStripped = true
			i++
			continue
		}
		if !titleStripped && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			titleStripped = true
			i++
			continue
		}
		break
	}
	body = strings.TrimLeft(strings.Join(lines[i:], "\n"), "\n")
	return title, body
}

// isThumbnailLine reports whether a single trimmed line is a standalone
// thumbnail image, in any of the representations the body can hold: the pipeline
// placeholder, a markdown image, or an <img> tag (optionally wrapped in a lone
// paragraph — the shape turndown emits for a top-of-document image).
func isThumbnailLine(trimmed string) bool {
	switch {
	case strings.Contains(trimmed, "{{IMAGE_THUMBNAIL}}"):
		return true
	case strings.HasPrefix(trimmed, "!["):
		return true
	case strings.HasPrefix(trimmed, "<img"):
		return true
	case strings.HasPrefix(trimmed, "<p") && strings.Contains(trimmed, "<img"):
		// A lone image wrapped in a paragraph: only strip it when the paragraph
		// holds nothing but the image (no caption text alongside it).
		return strings.TrimSpace(htmlTagRE.ReplaceAllString(trimmed, "")) == ""
	default:
		return false
	}
}

// ComposeArticleWithTitleAndThumbnail re-merges a separately-stored title and
// thumbnail back into one markdown block: the thumbnail image (when present) on
// the first line, then the H1 title, then the body. It is the canonical inverse
// of StripArticleTitleAndThumbnail — the review UI composes the same way
// client-side — and anchors the round-trip test. An empty thumbnailURL or title
// is omitted.
func ComposeArticleWithTitleAndThumbnail(title, thumbnailURL, thumbnailAlt, body string) string {
	var b strings.Builder
	if thumbnailURL != "" {
		b.WriteString("![")
		b.WriteString(thumbnailAlt)
		b.WriteString("](")
		b.WriteString(thumbnailURL)
		b.WriteString(")\n\n")
	}
	if title != "" {
		b.WriteString("# ")
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	return b.String()
}
