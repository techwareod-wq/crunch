package service

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
)

// markdownConverter renders the article body (CommonMark + GFM tables/strikethrough,
// the flavours the generation pipeline emits) to HTML. Reused across publishes —
// goldmark parsers are safe for concurrent use.
var markdownConverter = goldmark.New(goldmark.WithExtensions(extension.GFM))

// buildBlogPost maps the generated article (master context) onto the neutral
// BlogPost. All conversion happens here so the platform adapters receive
// ready-to-map values (per dto.BlogPost's contract): markdown is rendered to
// HTML and the thumbnail S3 key is resolved to an absolute URL.
//
// title is the canonical article title from the ScheduledArticle doc (the value
// the user edits); it maps onto the platform's own title field, while the body
// is published free of the title and thumbnail (both stored separately).
//
// resolveImageURL turns an S3 key into the absolute URL Framer will fetch; it is
// injected (rather than reaching for the s3 client here) so this stays a pure,
// unit-testable mapping.
//
// scheduleDate is the article's calendar slot; it maps onto the platform's date
// field so the CMS records the scheduled day, not the publish-run day. A zero
// value (the slot-less manual publish path) is left empty, and the sidecar falls
// back to the current time.
//
// focusKeyword is the triggering keyword's text (resolved by the caller from
// mc.KeywordID); empty when the lookup failed — the field is optional on every
// platform, so a miss degrades to "not mapped" rather than blocking a publish.
func buildBlogPost(mc *models.WebEntityMasterContext, title string, scheduleDate time.Time, focusKeyword string, resolveImageURL func(key string) string) dto.BlogPost {
	thumbURL, thumbAlt := thumbnail(mc, resolveImageURL)
	body := effectiveContent(mc)
	post := dto.BlogPost{
		Title:    effectiveTitle(mc, title),
		Slug:     effectiveSlug(mc),
		BodyHTML: markdownToHTML(body),
		// BodyMarkdown rides alongside BodyHTML: platforms with structured rich
		// text (Payload/Lexical) serialize from markdown in the adapter.
		BodyMarkdown:    body,
		MetaTitle:       safeMetaTitle(mc),
		MetaDescription: safeMetaDescription(mc),
		Excerpt:         safeExcerpt(mc),
		PublishDate:     formatPublishDate(scheduleDate),
		ThumbnailURL:    thumbURL,
		ThumbnailAlt:    thumbAlt,
		FocusKeyword:    focusKeyword,
		FAQ:             parseFAQ(mc.SchemaMarkup),
		// ImageURLs is intentionally left empty: mid-article images are already
		// embedded inline in the body markdown (image-replacement step), so they
		// render inside BodyHTML rather than as a separate gallery field.
		// TODO(schema): SchemaJSONLD = mc.SchemaMarkup (article + faq) once a
		// Framer field is designated for JSON-LD.
	}
	if mc.Publish != nil {
		post.RemoteItemID = mc.Publish.RemoteItemID // idempotent update key
	}
	return post
}

// parseFAQ extracts question/answer pairs from the generated FAQPage JSON-LD
// (mc.SchemaMarkup.FAQSchema). The markup is LLM-generated, so parsing is
// defensive: anything malformed or missing yields an empty slice — FAQ is an
// optional nicety, never a publish blocker.
func parseFAQ(sm *models.CGESchemaMarkup) []dto.FAQItem {
	if sm == nil || len(sm.FAQSchema) == 0 {
		return nil
	}
	var page struct {
		MainEntity []struct {
			Name           string `json:"name"`
			AcceptedAnswer struct {
				Text string `json:"text"`
			} `json:"acceptedAnswer"`
		} `json:"mainEntity"`
	}
	if err := json.Unmarshal(sm.FAQSchema, &page); err != nil {
		return nil
	}
	items := make([]dto.FAQItem, 0, len(page.MainEntity))
	for _, q := range page.MainEntity {
		if q.Name == "" || q.AcceptedAnswer.Text == "" {
			continue
		}
		items = append(items, dto.FAQItem{Question: q.Name, Answer: q.AcceptedAnswer.Text})
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

// formatPublishDate renders the scheduled calendar date as the RFC3339 string
// the sidecar maps onto the platform's date field. A zero time (no scheduled
// slot) yields an empty string, which the sidecar reads as "use now" — matching
// the pre-existing behaviour for the manual publish path.
func formatPublishDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// markdownToHTML renders article markdown to HTML. An empty body maps to an
// empty string (nothing to publish); a conversion failure — which goldmark does
// not produce for in-memory input in practice — falls back to the raw markdown
// so we publish something rather than a blank body.
func markdownToHTML(md string) string {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdownConverter.Convert([]byte(md), &buf); err != nil {
		return md
	}
	return buf.String()
}

// effectiveTitle returns the article title for the platform's title field. The
// canonical, user-facing title lives on the ScheduledArticle doc and is passed
// in; it falls back to the pipeline's ProposedTitle when the caller has no
// scheduled-article title (e.g. the manual publish path with no slot).
func effectiveTitle(mc *models.WebEntityMasterContext, title string) string {
	if title != "" {
		return title
	}
	return mc.ProposedTitle
}

// effectiveSlug prefers the user's edited URL slug, falling back to the slug
// picked out of the outline at generation time. The precedence now lives on
// the model (models.WebEntityMasterContext.EffectiveSlug) so the publish path
// and the normalized-URL stamp can never drift apart.
func effectiveSlug(mc *models.WebEntityMasterContext) string {
	return mc.EffectiveSlug()
}

// effectiveContent resolves the article body, mirroring the read-path precedence
// in scheduledArticleService: a saved user edit wins outright (even when empty —
// the user cleared it), otherwise the pipeline's FinalArticleContent, falling
// back to the post-image-replacement ArticleContent.
func effectiveContent(mc *models.WebEntityMasterContext) string {
	if mc.Edited != nil {
		return mc.Edited.ArticleContent
	}
	if mc.FinalArticleContent != "" {
		return mc.FinalArticleContent
	}
	return mc.ArticleContent
}

// thumbnail resolves the absolute URL and alt text of the article's thumbnail
// image — the mc.Images entry whose position is "thumbnail". Both are carried:
// unlike mid-article images (whose alt travels inline in the rendered body
// HTML), the thumbnail is a standalone field, so its alt has to ride alongside
// the URL. Returns empty strings when there is no thumbnail (the fields are then
// skipped by the platform adapter).
func thumbnail(mc *models.WebEntityMasterContext, resolveImageURL func(key string) string) (url, alt string) {
	for _, img := range mc.Images {
		if img.Position == "thumbnail" && img.S3Key != "" {
			return resolveImageURL(img.S3Key), img.Alt
		}
	}
	return "", ""
}

// safeMetaTitle resolves the user's edit over the pipeline meta assets. A
// non-nil Edited block takes precedence even when empty ("cleared by user").
func safeMetaTitle(mc *models.WebEntityMasterContext) string {
	if mc.Edited != nil {
		return mc.Edited.MetaTitle
	}
	if mc.MetaAssets != nil {
		return mc.MetaAssets.MetaTitle
	}
	return ""
}

func safeMetaDescription(mc *models.WebEntityMasterContext) string {
	if mc.Edited != nil {
		return mc.Edited.MetaDescription
	}
	if mc.MetaAssets != nil {
		return mc.MetaAssets.MetaDescription
	}
	return ""
}

func safeExcerpt(mc *models.WebEntityMasterContext) string {
	if mc.MetaAssets != nil {
		return mc.MetaAssets.SocialExcerpt
	}
	return ""
}
