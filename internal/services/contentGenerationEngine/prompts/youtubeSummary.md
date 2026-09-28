You are extracting useful insights from YouTube video transcripts on the topic of {{.Keyword}}.

Transcripts:
{{.CombinedTranscripts}}

Extract 2 concrete insights, examples, or practitioner tips from these transcripts
that would add genuine value to a blog article on this topic.

Rules:
- Only extract insights that are specific and concrete — not generic advice
- Do not fabricate anything not present in the transcripts
- Each insight should be something a reader would find genuinely useful or surprising
- Include the video title and channel for attribution

Return only valid JSON. No explanation. No markdown code blocks.
Start with { and end with }.

{
  "youtube_insights": [
    {
      "insight": "string — the specific insight or example in your own words",
      "video_title": "string",
      "channel": "string"
    }
  ]
}

