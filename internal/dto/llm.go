package dto

const (
	// AnthropicOpus48 is the Claude Opus 4.8 model identifier.
	AnthropicOpus48 = "claude-opus-4-8"
	// AnthropicSonnet5 is the Claude Sonnet 5 model identifier. Fixed id, no
	// date suffix. Adaptive thinking is ON when the request omits a thinking
	// config (unlike 4.6), and max_tokens caps thinking + response together —
	// size budgets with headroom.
	AnthropicSonnet5 = "claude-sonnet-5"
	// AnthropicSonnet46 is the Claude Sonnet 4.6 model identifier.
	AnthropicSonnet46 = "claude-sonnet-4-6"
	// AnthropicHaiku45 is the Claude Haiku 4.5 model identifier.
	AnthropicHaiku45 = "claude-haiku-4-5-20251001"
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
	// Images attaches base64-encoded images to this message (vision input).
	// They render as image content blocks BEFORE the text block. Only honored
	// by the Anthropic provider; others ignore them and send Content alone.
	Images []ImageBlock `json:"images,omitempty"`
	// Cache marks this message's content as a prompt-cache breakpoint: the
	// provider caches everything up to and including this block, so identical
	// prefixes across calls are served from cache (~10% of input price, and
	// exempt from Anthropic's input-tokens/min rate limit). Only honored by the
	// Anthropic provider; others ignore it. Content below the model's minimum
	// cacheable prefix (1024 tokens on Sonnet 4.6/Opus 4.8) is silently not
	// cached. Cached content must be byte-identical across calls to hit.
	Cache bool `json:"cache,omitempty"`
}

// ImageBlock is one vision-input image. MediaType must be one of the API's
// accepted types (image/jpeg, image/png, image/gif, image/webp); Data is the
// raw bytes base64-encoded (standard encoding, no data: prefix).
type ImageBlock struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type PromptRequest struct {
	Messages    []Message `json:"messages"`
	Model       string    `json:"model,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
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
}
