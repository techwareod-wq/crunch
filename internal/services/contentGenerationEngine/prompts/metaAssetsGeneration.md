Generate SEO meta assets for the following article.

Article title: {{.H1}}
First paragraph: {{.FirstParagraph}}
Target keyword: {{.Keyword}}
Article type: {{.ArticleType}}
Brand voice: {{.BrandVoiceTone}}
{{- if .ToneProfile}}

Style profile (learned from this publisher's existing articles — write the
meta assets in this exact voice; it wins over the brand voice line above):
{{.ToneProfile}}
{{- end}}

Return only valid JSON. No explanation. No markdown code blocks.
Start with { and end with }.

{
  "meta_title": "string — under 60 characters, keyword in first half",
  "meta_description": "string — 150 to 155 characters exactly, includes keyword, ends with hook or benefit",
  "social_excerpt": "string — 1 to 2 sentences for LinkedIn or Twitter, generates curiosity or conveys immediate value"
}