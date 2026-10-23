// Package groq is an LlmService over Groq's OpenAI-compatible chat
// completions API. It is built for latency-bound callers (AI search): one
// attempt, no backoff; the caller's ctx deadline bounds the call. Failures
// wrap the interfaces.ErrLLM* sentinels so callers can tell a rate limit, an
// outage and malformed output apart.
//
// A forced tool call (PromptRequest.ForceTool) runs in one of two modes:
//   - ModeJSON (default): JSON mode, with the tool's schema written into the
//     prompt; the reply object becomes the tool call's input. Groq validates
//     forced tool calls strictly against the schema, and gpt-oss often fails
//     that check (null for unused fields, missing empty arrays), so JSON mode
//     is the reliable path.
//   - ModeTool: a native forced function call.
//
// Either way the caller decodes and validates the input itself.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

const (
	// DefaultURL is Groq's chat completions endpoint.
	DefaultURL = "https://api.groq.com/openai/v1/chat/completions"
	// DefaultModel is used when neither the request nor New names one.
	DefaultModel = "openai/gpt-oss-120b"
	// ModeJSON / ModeTool pick how a forced tool call is made.
	ModeJSON = "json"
	ModeTool = "tool"
	// clientTimeout is a backstop only; callers bound the call with ctx.
	clientTimeout = 30 * time.Second
	// defaultCooldown / maxCooldown bound how long calls are skipped after a
	// 429, so a rate limit doesn't turn into a retry storm.
	defaultCooldown = 5 * time.Second
	maxCooldown     = 60 * time.Second
	// maxErrMsg caps the upstream error text kept in an error.
	maxErrMsg = 200
)

type groqService struct {
	apiKey           string
	apiURL           string
	defaultModel     string
	mode             string
	defaultMaxTokens int
	client           *http.Client
	now              func() time.Time

	mu           sync.Mutex
	blockedUntil time.Time
}

// New builds a client. An empty apiURL or model uses DefaultURL /
// DefaultModel; any mode but ModeTool is ModeJSON.
func New(apiKey, apiURL, model, mode string, maxTokens int) *groqService {
	if apiURL == "" {
		apiURL = DefaultURL
	}
	if model == "" {
		model = DefaultModel
	}
	if mode != ModeTool {
		mode = ModeJSON
	}
	return &groqService{apiKey: apiKey, apiURL: apiURL, defaultModel: model, mode: mode, defaultMaxTokens: maxTokens,
		client: &http.Client{Timeout: clientTimeout}, now: time.Now}
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Temperature         float64       `json:"temperature"`
	Tools               []chatTool    `json:"tools,omitempty"`
	ToolChoice          any           `json:"tool_choice,omitempty"`
	ResponseFormat      any           `json:"response_format,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatTool struct {
	Type     string       `json:"type"` // "function"
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (s *groqService) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	if wait := s.cooldownLeft(); wait > 0 {
		return nil, fmt.Errorf("groq: skipped, cooling down %.0fs after 429: %w", wait.Seconds(), interfaces.ErrLLMRateLimited)
	}
	body, jsonTool, err := s.buildRequest(req)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("groq: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("groq: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)

	start := s.now()
	resp, err := s.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("groq: %w", ctx.Err())
		}
		return nil, fmt.Errorf("groq: request failed: %w: %w", err, interfaces.ErrLLMUnavailable)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("groq: %w", ctx.Err())
		}
		return nil, fmt.Errorf("groq: read response: %w: %w", err, interfaces.ErrLLMUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s.statusError(resp, respBody)
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil || len(cr.Choices) == 0 {
		return nil, fmt.Errorf("groq: unparseable response: %w", interfaces.ErrLLMMalformed)
	}
	log.Debug("groq usage", "model", body.Model, "mode", s.mode, "promptTokens", cr.Usage.PromptTokens,
		"completionTokens", cr.Usage.CompletionTokens, "latencyMs", s.now().Sub(start).Milliseconds())

	choice := cr.Choices[0]
	out := &dto.PromptResponse{Content: choice.Message.Content, Model: cr.Model, Provider: "groq",
		PromptTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens, StopReason: choice.FinishReason}
	if choice.FinishReason == "length" {
		out.StopReason = dto.StopReasonMaxTokens
		return out, nil
	}
	switch {
	case jsonTool != "":
		if !isObject(choice.Message.Content) {
			return nil, fmt.Errorf("groq: reply is not a JSON object: %w", interfaces.ErrLLMMalformed)
		}
		out.ToolUse = &dto.ToolUse{Name: jsonTool, Input: json.RawMessage(strings.TrimSpace(choice.Message.Content))}
	case len(choice.Message.ToolCalls) > 0:
		call := choice.Message.ToolCalls[0].Function
		if !isObject(call.Arguments) {
			return nil, fmt.Errorf("groq: tool arguments are not a JSON object: %w", interfaces.ErrLLMMalformed)
		}
		out.ToolUse = &dto.ToolUse{Name: call.Name, Input: json.RawMessage(strings.TrimSpace(call.Arguments))}
	}
	if req.ForceTool != "" && (out.ToolUse == nil || out.ToolUse.Name != req.ForceTool) {
		return nil, fmt.Errorf("groq: no %s call: %w", req.ForceTool, interfaces.ErrLLMMalformed)
	}
	return out, nil
}

// buildRequest maps the DTO onto a chat completions request. In JSON mode a
// forced tool becomes a schema instruction plus response_format json_object;
// jsonTool then names that tool.
func (s *groqService) buildRequest(req dto.PromptRequest) (body chatRequest, jsonTool string, err error) {
	body = chatRequest{Model: req.Model, MaxCompletionTokens: req.MaxTokens}
	if body.Model == "" {
		body.Model = s.defaultModel
	}
	if body.MaxCompletionTokens <= 0 {
		body.MaxCompletionTokens = s.defaultMaxTokens
	}
	if req.Temperature != nil {
		body.Temperature = *req.Temperature
	}
	if req.DisableThinking {
		body.ReasoningEffort = reasoningOff(body.Model)
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}

	if req.ForceTool != "" && s.mode == ModeJSON {
		for _, t := range req.Tools {
			if t.Name == req.ForceTool {
				body.Messages = insertBeforeTurns(body.Messages, chatMessage{Role: dto.RoleSystem, Content: jsonInstruction(t)})
				body.ResponseFormat = map[string]string{"type": "json_object"}
				return body, t.Name, nil
			}
		}
		return body, "", fmt.Errorf("groq: forced tool %q not in tools", req.ForceTool)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, chatTool{Type: "function",
			Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	if req.ForceTool != "" {
		body.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": req.ForceTool}}
	}
	return body, "", nil
}

// jsonInstruction asks for the tool's arguments as the whole reply.
func jsonInstruction(t dto.ToolDef) string {
	return fmt.Sprintf("Instead of calling %s, reply with only its arguments as one JSON object (%s) matching this JSON Schema. "+
		"Use the property names exactly; leave out fields that don't apply.\n%s", t.Name, t.Description, t.InputSchema)
}

// insertBeforeTurns places m after the leading system messages.
func insertBeforeTurns(msgs []chatMessage, m chatMessage) []chatMessage {
	i := 0
	for i < len(msgs) && msgs[i].Role == dto.RoleSystem {
		i++
	}
	return append(msgs[:i:i], append([]chatMessage{m}, msgs[i:]...)...)
}

func isObject(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

// statusError maps a non-200 to an error: 429 starts the cooldown, 5xx is
// an outage, Groq's tool/JSON validation 400s are malformed output.
func (s *groqService) statusError(resp *http.Response, body []byte) error {
	var ae apiError
	_ = json.Unmarshal(body, &ae)
	msg := ae.Error.Message
	if len(msg) > maxErrMsg {
		msg = msg[:maxErrMsg]
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("retry-after"))
		s.startCooldown(wait)
		return fmt.Errorf("groq: rate limited (429), retry after %.0fs: %w", wait.Seconds(), interfaces.ErrLLMRateLimited)
	case resp.StatusCode >= 500:
		return fmt.Errorf("groq: server error (%d): %w", resp.StatusCode, interfaces.ErrLLMUnavailable)
	case ae.Error.Code == "tool_use_failed" || ae.Error.Code == "json_validate_failed":
		return fmt.Errorf("groq: %s: %w", ae.Error.Code, interfaces.ErrLLMMalformed)
	}
	return fmt.Errorf("groq: API error (%d): %s", resp.StatusCode, msg)
}

func (s *groqService) cooldownLeft() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockedUntil.Sub(s.now())
}

func (s *groqService) startCooldown(wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockedUntil = s.now().Add(wait)
}

// parseRetryAfter reads retry-after seconds (Groq may send fractions),
// defaulting and capping the cooldown.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || secs <= 0 {
		return defaultCooldown
	}
	return min(time.Duration(secs*float64(time.Second)), maxCooldown)
}

// reasoningOff is the lowest reasoning setting the model family accepts
// ("" = don't send the field).
func reasoningOff(model string) string {
	switch {
	case strings.HasPrefix(model, "openai/gpt-oss"):
		return "low"
	case strings.Contains(model, "qwen3"):
		return "none"
	}
	return ""
}
