package dto

import "encoding/json"

const (
	// AnthropicSonnet5 is the Claude Sonnet 5 model identifier. Fixed id, no
	// date suffix. Adaptive thinking is ON when the request omits a thinking
	// config (unlike 4.6), and max_tokens caps thinking + response together —
	// size budgets with headroom.
	AnthropicSonnet5 = "claude-sonnet-5"
)

// Message roles (Message.Role / PromptRequest message roles).
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// StopReasonMaxTokens is the PromptResponse.StopReason reported when
// generation was cut off at the output-token cap — callers parsing structured
// output should treat it as a truncated, unparseable response.
const StopReasonMaxTokens = "max_tokens"

type Message struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
	// Cache marks this message's content as a prompt-cache breakpoint: the
	// provider caches everything up to and including this block, so identical
	// prefixes across calls are served from cache (~10% of input price, and
	// exempt from Anthropic's input-tokens/min rate limit). Content below the model's minimum
	// cacheable prefix (1024 tokens on Sonnet 4.6/Opus 4.8) is silently not
	// cached. Cached content must be byte-identical across calls to hit.
	Cache bool `json:"cache,omitempty"`
}

type PromptRequest struct {
	Messages  []Message `json:"messages"`
	Model     string    `json:"model,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	// Temperature is sent only when set. Claude Sonnet 5 / Opus 4.7+ reject
	// any sampling parameter (400), so leave it nil for those models.
	Temperature *float64 `json:"temperature,omitempty"`
	// Tools the model may call. ForceTool names one of them: the model must
	// answer with exactly that tool call (structured extraction).
	Tools     []ToolDef `json:"tools,omitempty"`
	ForceTool string    `json:"force_tool,omitempty"`
	// DisableThinking turns adaptive thinking off (Sonnet 5 thinks by
	// default). Needed for low-latency forced-tool extraction.
	DisableThinking bool `json:"disable_thinking,omitempty"`
}

// ToolDef is a client tool: name, description and a JSON Schema for its
// input.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolUse is a tool call the model made. Input is the raw JSON arguments;
// always json.Unmarshal it (escaping varies by model).
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type PromptResponse struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	PromptTokens int    `json:"prompt_tokens"`
	OutputTokens int    `json:"output_tokens"`
	// StopReason is the provider's reason for ending generation (e.g.
	// "end_turn", "max_tokens"). Empty if the provider doesn't report it.
	// Callers parsing structured output should treat "max_tokens" as a
	// truncated, unparseable response rather than a malformed one.
	StopReason string `json:"stop_reason"`
	// ToolUse is the first tool call in the response, if any.
	ToolUse *ToolUse `json:"tool_use,omitempty"`
}
