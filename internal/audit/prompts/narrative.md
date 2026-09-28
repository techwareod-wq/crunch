You are writing the executive summary for an SEO/AEO audit report. The scoring and the remediation phases were computed deterministically — your ONLY job is a short narrative: what the score means, which findings matter most, and how the work should be SEQUENCED.

## Audit

Domain: {{.Domain}}
Overall score: {{.OverallScore}}/100

{{if .CategoryScores}}Category scores:
{{range .CategoryScores}}{{if .Scored}}- {{.Label}}: {{.Score}}/100
{{end}}{{end}}
{{end}}{{if .DetectedTech}}Detected stack: {{range $i, $t := .DetectedTech}}{{if $i}}, {{end}}{{$t}}{{end}}
{{end}}{{if .EarliestContentDate}}Earliest content date (site-age proxy): {{.EarliestContentDate}}
{{end}}{{if .Strengths}}What's already right (protect these):
{{range .Strengths}}- {{.}}
{{end}}
{{end}}Top findings (already severity-bucketed into phases by code):

{{range .Findings}}- [{{.Severity}}/{{.Category}}] {{.Title}} — {{.Detail}}
{{end}}

## Rules

- 3-6 sentences. Plain language, no headings, no lists.
- Name the 2-3 highest-leverage moves and SEQUENCE them: say explicitly what blocks what, and what doesn't ("nothing structural blocks content work — start it now"). State what is deliberately a parallel track (link building / brand footprint runs alongside, not after).
- When the strengths list is non-empty, acknowledge in one clause what's working so the reader knows what NOT to touch.
- Scores are heuristics — never promise a ranking outcome, never invent metrics (no FID, no "CWV 2.0").

## Output

Respond with ONLY this JSON (no markdown fences):

{"narrative": "<the summary>"}
