Generate 5 distinct, catchy, SEO-optimized blog article title options.

Target keyword: {{.Keyword}}
Article type (use as a hint for which pattern fits, NOT a forced opening phrase): {{.ArticleType}}
Target reader: {{.ICPRole}}
Business: {{.BusinessName}}
Funnel stage: {{.Funnel}}
{{- if .TitlePattern}}

Title pattern (learned from this publisher's EXISTING TITLES — match its
casing, shape, punctuation, and specificity habits, within every rule below):
{{.TitlePattern}}
{{- else if .ToneProfile}}

Style profile (learned from this publisher's existing articles — phrase the
title in this voice, within every rule below):
{{.ToneProfile}}
{{- end}}

Catchiness comes from specificity and a concrete payload — never from hype words.
Across the 5 options, draw from these proven patterns (vary which pattern each one uses):

1. Topic + colon + concrete rule-of-three payload
   "[Topic]: [Value 1], [Value 2], and [Value 3]"
   e.g. "Customer Service Automation: Use Cases, Tools, and How to Do It Well"
2. Number-led list with audience or use case
   "[N] Best [Things] for [Audience / Use Case]"
   e.g. "5 Best Finance AI Chatbots for Banks and Personal Finance"
3. Comparison / alternatives framing
   "The Best [X] Alternatives for [Audience / Use Case]"
   e.g. "The Best Fin Alternatives for Enterprise Customer Support"
4. Natural question + payoff (only when it reads naturally)
   "What Is [X]? How It Works and How to Use It [+ year if time-sensitive]"
   e.g. "What Is a Voicebot? How They Work and How to Build One in 2026"
5. How-to with a concrete outcome (optional honest credibility hook)
   "How to [Outcome] in [Year]" or "How To [Outcome] (I Tested the Top Tools)"

Rules for every title:
- Front-load the target keyword; it must appear completely and naturally, ideally near the start.
- Lead with the reader's value: name the actual things they get (use cases, tools,
  pricing, alternatives, steps, comparisons) instead of vague promises.
- Use the article type only to choose which pattern fits. Do NOT mechanically start
  every What-Is article with "What Is" or every guide with "How to" when a stronger,
  still-accurate pattern fits better.
- A natural question is allowed when it is the clearest phrasing. No clickbait questions
  whose answer isn't implied by the title.
- A year ("in 2026" / "[2026]") is allowed and encouraged for fast-moving, pricing,
  "best/top", or comparison topics. Skip it for evergreen definitions.
- Specificity wins: prefer numbers, named outcomes, audiences, and comparisons over
  generic category labels.
- Under 65 characters preferred, never exceed 70.
- Match funnel: BOFU = specific + decision/outcome; MOFU = reader context or use case;
  TOFU = clear and searchable.
- Do not include the business name in the title.
- Banned hype: "Ultimate Guide", "Everything You Need to Know", "Complete Guide",
  "Game Changer", "Unlock", "Master", "Seamless", "Robust", "Cutting-edge", "Leverage".

The 5 options must be meaningfully different from each other — vary the pattern, angle,
and hook, not just word order. Aim for distinct angles such as:
  1. Pain or risk angle
  2. Outcome or benefit angle
  3. Comparison or choice angle
  4. Mistake or trade-off angle
  5. Audience or use-case angle
Do not force an angle if it would violate the rules or sound unnatural.

Silent quality checks before returning:
- Rule of One: each title promises one clear idea, decision, or outcome.
- SERP test: each title should look meaningfully more specific than the other 9 results.
  If one reads like a bare "What Is X" or "How to X" with nothing added, rewrite it with
  a concrete payload, number, audience, or comparison.
- No rereading required, no exaggerated claims, no keyword stuffing.

Return only valid JSON. No explanation outside JSON. No markdown formatting.
Do not wrap in code blocks. Start with { and end with }.

{
  "titles": [
    "string — title option 1",
    "string — title option 2",
    "string — title option 3",
    "string — title option 4",
    "string — title option 5"
  ]
}
