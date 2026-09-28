=== ARTICLE PARAMETERS ===

Target keyword: {{.Keyword}}
{{- if .SecondaryKeywords}}
Secondary keywords (distribute naturally throughout): {{.SecondaryKeywords}}
{{- end}}

Article type: {{.ArticleType}}
Title (H1): {{.ProposedTitle}}
Target word count: {{.TargetWordCount}}
{{- if .ICPRole}}
Target reader: {{.ICPRole}}
{{- end}}
{{- if .ICPPainPoints}}
Reader pain point: {{.ICPPainPoints}}
{{- end}}
{{- if .AdditionalInstructions}}

=== ADDITIONAL INSTRUCTIONS (USER-SUPPLIED, TREAT AS HIGH PRIORITY) ===

{{.AdditionalInstructions}}
{{- end}}

{{- if .ToneProfile}}
=== STYLE PROFILE (learned from this publisher's existing articles) ===

Write in this exact voice. Where anything below under BRAND VOICE conflicts
with it, this style profile wins.

{{.ToneProfile}}

{{end -}}
=== BRAND VOICE ===

Tone: {{.BrandVoice}}

=== BUSINESS CONTEXT ===

Business: {{.BusinessName}}
Product: {{.ProductType}}
Key differentiator: {{.KeyDifferentiator}}
{{- if .KeyFeatures}}
Key features: {{.KeyFeatures}}
{{- end}}

=== TOPIC RESEARCH (use as background knowledge only, do not cite) ===

{{- if .TopicResearchNews}}
Recent news: {{.TopicResearchNews}} (Source: {{.TopicResearchNewsSourceName}})
{{- end}}
{{- if .TopicResearchExpert}}
Expert opinion: {{.TopicResearchExpert}} (Source: {{.TopicResearchExpertSourceName}})
{{- end}}
{{- if .TopicResearchMistakes}}
Common mistake: {{.TopicResearchMistakes}} (Source: {{.TopicResearchMistakesSourceName}})
{{- end}}
{{- if .YouTubeInsights}}
YouTube insights: {{.YouTubeInsights}}
{{- end}}

Use these where they fit naturally as background knowledge. Do not force all of
them in. Do not fabricate any statistics or claims beyond what is provided above.
Do not attribute these to a source or name them in the article.
- If a research item gives only qualitative evidence, make a qualitative claim. Do
  not turn it into a numeric or universal claim.

=== SERP DIFFERENTIATION ===

What competitors are already covering (do not just repeat these):
{{.CoveredByAll}}

Gaps to fill that competitors are missing:
{{.GapsIdentified}}

Differentiating angle for this article:
{{.DifferentiatingAngle}}

Featured snippet opportunity (answer this directly and concisely in the relevant section):
{{.FeaturedSnippetOpp}}

=== FUNNEL STAGE ===

Funnel stage: {{.Funnel}}

=== ARTICLE STRUCTURE ===

Follow this outline exactly. Every H2 and H3 must appear in the order specified.
Word counts per section are targets. Stay within 10% of each unless doing so
would push the final article outside the 1500-2500 word range.

{{.FullOutlineJSON}}

Now write the article following every instruction above. Start your response with
{{"{{IMAGE_THUMBNAIL}}"}} on the very first line.
