package groq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var schema = json.RawMessage(`{"type":"object","properties":{"chips":{"type":"array","items":{"type":"string"}}}}`)

func request() dto.PromptRequest {
	return dto.PromptRequest{
		MaxTokens: 1024, DisableThinking: true,
		Messages: []dto.Message{
			{Role: dto.RoleSystem, Content: "rules", Cache: true},
			{Role: dto.RoleSystem, Content: "hints"},
			{Role: dto.RoleUser, Content: "cold storage in Pune"},
		},
		Tools:     []dto.ToolDef{{Name: "set_filters", Description: "Set filters.", InputSchema: schema}},
		ForceTool: "set_filters",
	}
}

// server answers every call with status and body, counting the calls.
func server(t *testing.T, status int, body string, hdr map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func jsonReply(content, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"model":   DefaultModel,
		"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": 1100, "completion_tokens": 60},
	})
	return string(b)
}

func toolReply(args string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
			"tool_calls": []any{map[string]any{"function": map[string]any{"name": "set_filters", "arguments": args}}}}}},
	})
	return string(b)
}

// capture answers with reply and keeps the decoded request.
func capture(t *testing.T, reply string, got *map[string]any, auth *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(got)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPromptJSONMode(t *testing.T) {
	var got map[string]any
	var auth string
	srv := capture(t, jsonReply(` {"chips":["cold_storage"]} `, "stop"), &got, &auth)

	resp, err := New("test-key", srv.URL, "", "", 0).Prompt(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolUse == nil || resp.ToolUse.Name != "set_filters" || string(resp.ToolUse.Input) != `{"chips":["cold_storage"]}` {
		t.Errorf("tool use = %+v", resp.ToolUse)
	}
	if resp.PromptTokens != 1100 || resp.OutputTokens != 60 || resp.Provider != "groq" {
		t.Errorf("resp = %+v", resp)
	}
	if auth != "Bearer test-key" {
		t.Errorf("auth header = %q", auth)
	}
	if got["model"] != DefaultModel || got["temperature"] != 0.0 || got["max_completion_tokens"] != 1024.0 || got["reasoning_effort"] != "low" {
		t.Errorf("request = %v", got)
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_object" || got["tools"] != nil || got["tool_choice"] != nil {
		t.Errorf("json mode request = %v", got)
	}
	// The schema instruction sits after the system messages, before the
	// user turn, carrying the schema unchanged.
	msgs := got["messages"].([]any)
	third := msgs[2].(map[string]any)
	if len(msgs) != 4 || third["role"] != "system" || !strings.Contains(third["content"].(string), string(schema)) ||
		msgs[3].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestPromptToolMode(t *testing.T) {
	var got map[string]any
	var auth string
	srv := capture(t, toolReply(`{"chips":["cold_storage"]}`), &got, &auth)

	resp, err := New("k", srv.URL, "", ModeTool, 0).Prompt(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolUse == nil || string(resp.ToolUse.Input) != `{"chips":["cold_storage"]}` {
		t.Errorf("tool use = %+v", resp.ToolUse)
	}
	tc, _ := json.Marshal(got["tool_choice"])
	if string(tc) != `{"function":{"name":"set_filters"},"type":"function"}` || got["response_format"] != nil {
		t.Errorf("request = %v", got)
	}
	// The Anthropic input_schema is carried over as OpenAI parameters.
	params, _ := json.Marshal(got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"])
	var a, b any
	_ = json.Unmarshal(params, &a)
	_ = json.Unmarshal(schema, &b)
	if pa, pb := mustJSON(a), mustJSON(b); pa != pb {
		t.Errorf("parameters = %s", pa)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPromptMalformed(t *testing.T) {
	for name, tc := range map[string]struct {
		mode, body string
	}{
		"not json":            {ModeJSON, `{"choices": [`},
		"json: bad content":   {ModeJSON, jsonReply(`{"chips": ["cold`, "stop")},
		"json: prose":         {ModeJSON, jsonReply(`cold storage`, "stop")},
		"json: array":         {ModeJSON, jsonReply(`["cold_storage"]`, "stop")},
		"tool: bad arguments": {ModeTool, toolReply(`{"chips": ["cold`)},
		"tool: no tool call":  {ModeTool, jsonReply(`cold storage`, "stop")},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := server(t, http.StatusOK, tc.body, nil)
			if _, err := New("k", srv.URL, "", tc.mode, 0).Prompt(context.Background(), request()); !errors.Is(err, interfaces.ErrLLMMalformed) {
				t.Errorf("err = %v", err)
			}
		})
	}
	for _, code := range []string{"tool_use_failed", "json_validate_failed"} {
		t.Run(code, func(t *testing.T) {
			srv, _ := server(t, http.StatusBadRequest, `{"error":{"message":"failed","code":"`+code+`"}}`, nil)
			if _, err := New("k", srv.URL, "", "", 0).Prompt(context.Background(), request()); !errors.Is(err, interfaces.ErrLLMMalformed) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestPromptTruncated(t *testing.T) {
	srv, _ := server(t, http.StatusOK, jsonReply(`{"chips":[`, "length"), nil)
	resp, err := New("k", srv.URL, "", "", 0).Prompt(context.Background(), request())
	if err != nil || resp.StopReason != dto.StopReasonMaxTokens {
		t.Errorf("resp = %+v err = %v", resp, err)
	}
}

func TestPromptRateLimitedCoolsDown(t *testing.T) {
	srv, calls := server(t, http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached"}}`, map[string]string{"retry-after": "7"})
	g := New("k", srv.URL, "", "", 0)
	now := time.Now()
	g.now = func() time.Time { return now }

	if _, err := g.Prompt(context.Background(), request()); !errors.Is(err, interfaces.ErrLLMRateLimited) {
		t.Fatalf("err = %v", err)
	}
	// Inside retry-after: no call reaches Groq.
	now = now.Add(6 * time.Second)
	if _, err := g.Prompt(context.Background(), request()); !errors.Is(err, interfaces.ErrLLMRateLimited) || calls.Load() != 1 {
		t.Fatalf("err = %v calls = %d", err, calls.Load())
	}
	// After it: one more try.
	now = now.Add(2 * time.Second)
	_, _ = g.Prompt(context.Background(), request())
	if calls.Load() != 2 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestPromptServerError(t *testing.T) {
	srv, calls := server(t, http.StatusServiceUnavailable, `{"error":{"message":"over capacity"}}`, nil)
	if _, err := New("k", srv.URL, "", "", 0).Prompt(context.Background(), request()); !errors.Is(err, interfaces.ErrLLMUnavailable) || calls.Load() != 1 {
		t.Errorf("err = %v calls = %d", err, calls.Load())
	}
}

func TestPromptTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New("k", srv.URL, "", "", 0).Prompt(ctx, request())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
}

func TestReasoningOff(t *testing.T) {
	for model, want := range map[string]string{"openai/gpt-oss-120b": "low", "qwen/qwen3-32b": "none", "llama-3.3-70b-versatile": ""} {
		if got := reasoningOff(model); got != want {
			t.Errorf("%s: %q", model, got)
		}
	}
}
