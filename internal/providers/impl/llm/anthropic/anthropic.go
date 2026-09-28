package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

const (
	// defaultRequestTimeout bounds a single Messages API call when the values
	// file doesn't set one. Must stay under the SQS clustering visibility
	// override (900s) so a slow call can't outlive its message lease.
	defaultRequestTimeout = 10 * time.Minute
	// maxAttempts is the total number of tries per Prompt call (1 initial +
	// retries). Retries only fire on 429/5xx/transport errors; the async
	// handler's own re-enqueue retry sits above this.
	maxAttempts = 4
	// maxRetryWait caps how long a single retry-after backoff can sleep, so a
	// pathological header can't stall a worker slot.
	maxRetryWait = 60 * time.Second
)

type anthropicService struct {
	apiKey            string
	apiURL            string
	apiVersion        string
	fallbackModel     string
	fallbackMaxTokens int
	client            *http.Client

	// limitsLogged tracks which models have had their org rate limits logged
	// (once per model per process) so prod logs reveal the actual Anthropic
	// thresholds without console access.
	mu           sync.Mutex
	limitsLogged map[string]struct{}
}

func New(apiKey, apiURL, apiVersion, fallbackModel string, fallbackMaxTokens, requestTimeoutSeconds int) *anthropicService {
	timeout := defaultRequestTimeout
	if requestTimeoutSeconds > 0 {
		timeout = time.Duration(requestTimeoutSeconds) * time.Second
	}
	return &anthropicService{
		apiKey:            apiKey,
		apiURL:            apiURL,
		apiVersion:        apiVersion,
		fallbackModel:     fallbackModel,
		fallbackMaxTokens: fallbackMaxTokens,
		client:            &http.Client{Timeout: timeout},
		limitsLogged:      map[string]struct{}{},
	}
}

type messagesRequest struct {
	Model     string         `json:"model"`
	Messages  []apiMessage   `json:"messages"`
	System    []contentBlock `json:"system,omitempty"`
	MaxTokens int            `json:"max_tokens"`
}

type apiMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	Source       *imageSource  `json:"source,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// imageSource is the base64 source of an image content block (vision input).
type imageSource struct {
	Type      string `json:"type"` // always "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type cacheControl struct {
	Type string `json:"type"` // always "ephemeral"
}

// maxCacheBreakpoints is Anthropic's per-request cap on cache_control markers.
const maxCacheBreakpoints = 4

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens             int `json:"input_tokens"`
		OutputTokens            int `json:"output_tokens"`
		CacheCreationInputToken int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens    int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *anthropicService) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	model := req.Model
	if model == "" {
		model = s.fallbackModel
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = s.fallbackMaxTokens
	}

	system, msgs := buildContent(req.Messages)

	body := messagesRequest{
		Model:     model,
		Messages:  msgs,
		System:    system,
		MaxTokens: maxTokens,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: failed to marshal request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, retryable, err := s.doRequest(ctx, payload, model, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("anthropic: giving up after %d attempts: %w", maxAttempts, lastErr)
}

// buildContent converts DTO messages into the API's block-based shape.
// System messages become system blocks; consecutive same-role messages merge
// into one API message with multiple content blocks (the API rejects
// consecutive same-role messages, and multi-block lets a cached static prefix
// and uncached dynamic suffix ride the same user turn). A message's Images
// render as image blocks before its text block (vision input). A message's
// Cache flag becomes a cache_control breakpoint on its LAST block — the
// breakpoint covers everything up to and including it — capped at the API's
// limit of 4 per request (extras are dropped with a warning).
func buildContent(messages []dto.Message) ([]contentBlock, []apiMessage) {
	breakpoints := 0
	blocks := func(m dto.Message) []contentBlock {
		var out []contentBlock
		for _, img := range m.Images {
			out = append(out, contentBlock{
				Type:   "image",
				Source: &imageSource{Type: "base64", MediaType: img.MediaType, Data: img.Data},
			})
		}
		if m.Content != "" || len(out) == 0 {
			out = append(out, contentBlock{Type: "text", Text: m.Content})
		}
		if m.Cache {
			if breakpoints < maxCacheBreakpoints {
				out[len(out)-1].CacheControl = &cacheControl{Type: "ephemeral"}
				breakpoints++
			} else {
				log.Warn("anthropic: cache breakpoint limit reached, marker dropped",
					"limit", maxCacheBreakpoints)
			}
		}
		return out
	}

	var system []contentBlock
	var msgs []apiMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, blocks(m)...)
			continue
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == m.Role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks(m)...)
			continue
		}
		msgs = append(msgs, apiMessage{Role: m.Role, Content: blocks(m)})
	}
	return system, msgs
}

// doRequest performs one HTTP attempt. On a retryable failure (429/529/5xx or
// transport error) it sleeps the backoff itself (honoring retry-after and ctx
// cancellation) before returning, so the caller loop can retry immediately.
func (s *anthropicService) doRequest(ctx context.Context, payload []byte, model string, attempt int) (*dto.PromptResponse, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("anthropic: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", s.apiKey)
	httpReq.Header.Set("anthropic-version", s.apiVersion)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		// Transport failure (timeout, connection reset). Retry with backoff
		// unless the context itself is done.
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("anthropic: request failed: %w", err)
		}
		s.sleepBackoff(ctx, attempt, 0)
		return nil, true, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		s.sleepBackoff(ctx, attempt, 0)
		return nil, true, fmt.Errorf("anthropic: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		reqErr := fmt.Errorf("anthropic: API error (%d): %s", resp.StatusCode, apiErr.Error.Message)

		// 429 = rate limited, 529 = overloaded, 5xx = transient server error.
		// Everything else (400/401/403/...) won't change on retry.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
			if resp.StatusCode == http.StatusTooManyRequests {
				log.Warn("anthropic rate limited (429)",
					"model", model,
					"attempt", attempt,
					"retryAfterSeconds", retryAfter.Seconds(),
					"requestsRemaining", resp.Header.Get("anthropic-ratelimit-requests-remaining"),
					"inputTokensLimit", resp.Header.Get("anthropic-ratelimit-input-tokens-limit"),
					"inputTokensRemaining", resp.Header.Get("anthropic-ratelimit-input-tokens-remaining"),
					"outputTokensLimit", resp.Header.Get("anthropic-ratelimit-output-tokens-limit"),
					"outputTokensRemaining", resp.Header.Get("anthropic-ratelimit-output-tokens-remaining"),
				)
			} else {
				log.Warn("anthropic transient server error, retrying",
					"model", model,
					"status", resp.StatusCode,
					"attempt", attempt,
				)
			}
			s.sleepBackoff(ctx, attempt, retryAfter)
			return nil, true, reqErr
		}
		return nil, false, reqErr
	}

	s.logRateLimitsOnce(model, resp.Header)

	var msgResp messagesResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return nil, false, fmt.Errorf("anthropic: failed to parse response: %w", err)
	}

	if len(msgResp.Content) == 0 {
		return nil, false, fmt.Errorf("anthropic: no content returned")
	}

	if msgResp.Usage.CacheReadInputTokens > 0 || msgResp.Usage.CacheCreationInputToken > 0 {
		log.Info("anthropic prompt cache",
			"model", model,
			"cacheReadTokens", msgResp.Usage.CacheReadInputTokens,
			"cacheWriteTokens", msgResp.Usage.CacheCreationInputToken,
			"uncachedInputTokens", msgResp.Usage.InputTokens,
		)
	}

	// Models with adaptive thinking (Sonnet 5+) prepend a "thinking" block to
	// the content array — the answer lives in the "text" block(s). Concatenate
	// those; fall back to the first block so responses without typed text
	// blocks keep the old behavior.
	content := ""
	for _, b := range msgResp.Content {
		if b.Type == "text" {
			content += b.Text
		}
	}
	if content == "" {
		content = msgResp.Content[0].Text
	}

	return &dto.PromptResponse{
		Content:  content,
		Model:    msgResp.Model,
		Provider: "anthropic",
		// Cache writes count toward Anthropic's input-tokens/min limit (and
		// bill at 1.25x), so include them; cache reads count toward neither,
		// so they are deliberately excluded from the tracked total.
		PromptTokens: msgResp.Usage.InputTokens + msgResp.Usage.CacheCreationInputToken,
		OutputTokens: msgResp.Usage.OutputTokens,
		StopReason:   msgResp.StopReason,
	}, false, nil
}

// logRateLimitsOnce logs the org's Anthropic rate limits the first time each
// model responds, so the actual thresholds (per model, per token direction)
// are discoverable from service logs.
func (s *anthropicService) logRateLimitsOnce(model string, h http.Header) {
	if h.Get("anthropic-ratelimit-input-tokens-limit") == "" &&
		h.Get("anthropic-ratelimit-tokens-limit") == "" {
		return
	}
	s.mu.Lock()
	_, seen := s.limitsLogged[model]
	if !seen {
		s.limitsLogged[model] = struct{}{}
	}
	s.mu.Unlock()
	if seen {
		return
	}
	log.Info("anthropic org rate limits discovered",
		"model", model,
		"requestsPerMinLimit", h.Get("anthropic-ratelimit-requests-limit"),
		"inputTokensPerMinLimit", h.Get("anthropic-ratelimit-input-tokens-limit"),
		"outputTokensPerMinLimit", h.Get("anthropic-ratelimit-output-tokens-limit"),
		"combinedTokensLimit", h.Get("anthropic-ratelimit-tokens-limit"),
	)
}

// sleepBackoff waits before the next attempt: the server-provided retry-after
// when present, otherwise exponential backoff (2s, 4s, 8s), capped at
// maxRetryWait. Returns early if ctx is cancelled.
func (s *anthropicService) sleepBackoff(ctx context.Context, attempt int, retryAfter time.Duration) {
	wait := retryAfter
	if wait <= 0 {
		wait = time.Duration(1<<attempt) * time.Second
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
