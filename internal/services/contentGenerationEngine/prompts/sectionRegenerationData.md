Article context:
- Title: {{.Title}}
- Target keyword: {{.Keyword}}
- Secondary keywords: {{.SecondaryKeywords}}
- Article type: {{.ArticleType}}
- Funnel stage: {{.Funnel}}
- Audience (ICP roles): {{.ICPRole}}
- Brand voice: {{.BrandVoice}}
- Business: {{.BusinessName}}
{{- if .ToneProfile}}

Style profile (learned from this publisher's existing articles — write the
rewrite in this exact voice; it wins over the brand voice line above):
{{.ToneProfile}}
{{- end}}

Article outline (JSON):
{{.OutlineJSON}}

Rewrite request from the article owner (empty means "produce a fresh improved version"):
{{.Instructions}}

<selected_passage>
{{.SelectedMarkdown}}
</selected_passage>

Rewrite the passage following the instructions you were given. Return only the JSON object.
