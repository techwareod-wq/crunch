Article parameters:
- Target keyword: {{.Keyword}}
- Secondary keywords to distribute naturally: 
{{- if .SecondaryKeywords}}
-- {{.SecondaryKeywords}}
{{- end}}
- Article type: {{.ArticleType}}
- Proposed title: {{.ProposedTitle}}
- Target word count: {{.TargetWordCount}}
- Effective article word count: choose a final budget between 1500 and 2500 words.
  If the target word count or competitor average is higher than 2500, cap the
  outline at 2500. If the target is lower than 1500, raise the outline to 1500.
- Target reader: 
{{- if .ICPRole}}
-- {{.ICPRole}}
{{- end}}
- Reader pain point: 
{{- if .ICPPainPoints}}
-- {{.ICPPainPoints}}
{{- end}}
- Brand voice: {{.BrandVoice}}
{{- if .StructurePattern}}

Structure pattern (learned from this publisher's existing articles — steer the
hook style, section count and length rhythm, H2 phrasing, and list/FAQ/CTA
habits to match it, while keeping the required outline JSON schema exactly as
specified above):
{{.StructurePattern}}
{{- end}}
{{- if .ToneProfile}}

Heading voice hint: phrase the H2/H3 headings in this publisher's voice (this
profile guides wording only — never structure):
{{.ToneProfile}}
{{- end}}

Business context:
- Business: {{.BusinessName}}
- Product: {{.ProductType}}
- Key differentiator: {{.KeyDifferentiator}}

Content strategy:
- Differentiating angle: {{.DifferentiatingAngle}}
- Content gaps to address: {{.GapsIdentified}}
- Featured snippet opportunity: {{.FeaturedSnippetOpp}}
- Average competitor word count: {{.AverageWordCount}}

Topic research available to assign to sections:
{{- if .TopicResearchNews}}
{{.TopicResearchNews}}
{{- end}}
{{- if .TopicResearchExpert}}
{{.TopicResearchExpert}}
{{- end}}
{{- if .TopicResearchMistakes}}
{{.TopicResearchMistakes}}
{{- end}}


{{- if .YouTubeInsights}}
YouTube insights available to assign to sections:
-{{.YouTubeInsights}}
{{- end}}

Now produce the outline following every rule above. Return only the JSON object
specified above. Start with { and end with }.
