// Package prompts embeds the audit engine's two LLM templates. Neither is
// prompt-cached: each runs once per audit with fully dynamic content.
package prompts

import _ "embed"

//go:embed content_judgment.md
var ContentJudgment string

//go:embed narrative.md
var Narrative string
