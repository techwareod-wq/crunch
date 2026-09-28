You are adding internal links to a finished blog article.

Goal:
Add 2-5 natural internal links from the provided sitemap URLs into the article markdown.
The article was written with no links. Some sentences may contain plain-text CTAs
(for example "book a call", "contact us", "sign up", "get started", or a free tool).
Where such a CTA appears and a matching sitemap URL exists, turn that existing CTA
text into a link without changing its wording.

Target keyword: {{.Keyword}}
Article title: {{.Title}}
Business website: {{.WebsiteURL}}

Sitemap URLs:
{{.SitemapURLs}}

Article markdown:
{{.ArticleMarkdown}}

Rules:
- Add 2-5 internal links total (contextual links plus any CTA links combined).
- Only use URLs from the sitemap list.
- Only add a link when the target URL is genuinely relevant to the surrounding sentence or section.
- Use natural anchor text already present in the article where possible.
- If the article already contains a plain-text CTA (book a call, contact, sign up,
  get started, demo, free tool, etc.) and a matching sitemap URL exists (such as a
  contact, booking, pricing, signup, or tool page), link that exact existing CTA text.
- Never change, rephrase, or move the article wording. Only wrap existing text in a link.
- If a CTA has no matching sitemap URL, leave it as plain text.
- Do not force links.
- If fewer than 2 links are genuinely relevant, add fewer than 2.
- Do not add links in the first paragraph.
- Do not add links in the H1.
- Do not add links inside image placeholders.
- Preserve {{"{{IMAGE_THUMBNAIL}}"}} exactly.
- Preserve {{"{{IMAGE_MID_ARTICLE}}"}} exactly.
- Do not rewrite the article broadly. Only add internal links and make tiny grammar fixes if needed.
- Do not link the same URL more than once.
- Do not use generic anchors like "click here", "read more", or "this page".
- If the sitemap contains irrelevant URLs like login, privacy, tag, category, author, or legal pages, ignore them.

Return only valid JSON. No markdown fences. No explanation.

{
  "article_markdown": "full article markdown with internal links inserted",
  "links_added": [
    {
      "url": "string",
      "anchor_text": "string",
      "reason": "string"
    }
  ]
}
