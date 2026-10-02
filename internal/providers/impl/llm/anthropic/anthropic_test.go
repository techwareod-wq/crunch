package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
)

func TestForcedToolRequestAndParse(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Write([]byte(`{"model":"claude-sonnet-5","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},
			"content":[{"type":"tool_use","id":"t1","name":"set_filters","input":{"chips":["cold_storage"]}}]}`))
	}))
	defer srv.Close()
	c := New("k", srv.URL, "2023-06-01", "m", 100, 5, 1)
	resp, err := c.Prompt(context.Background(), dto.PromptRequest{
		Model: dto.AnthropicSonnet5, MaxTokens: 512, DisableThinking: true, ForceTool: "set_filters",
		Tools:    []dto.ToolDef{{Name: "set_filters", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []dto.Message{{Role: dto.RoleSystem, Content: "rules", Cache: true}, {Role: dto.RoleUser, Content: "cold storage pune"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolUse == nil || resp.ToolUse.Name != "set_filters" || string(resp.ToolUse.Input) != `{"chips":["cold_storage"]}` {
		t.Errorf("tool use = %+v", resp.ToolUse)
	}
	if tc := got["tool_choice"].(map[string]any); tc["type"] != "tool" || tc["name"] != "set_filters" {
		t.Errorf("tool_choice = %v", tc)
	}
	if th := got["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("thinking = %v", th)
	}
	if _, has := got["temperature"]; has {
		t.Error("temperature sent although unset")
	}
	sys := got["system"].([]any)[0].(map[string]any)
	if sys["cache_control"] == nil {
		t.Error("system block not cached")
	}
}

func TestSingleAttemptDoesNotRetryOrSleep(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("retry-after", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	start := time.Now()
	_, err := New("k", srv.URL, "v", "m", 100, 5, 1).Prompt(context.Background(), dto.PromptRequest{Messages: []dto.Message{{Role: dto.RoleUser, Content: "x"}}})
	if err == nil || calls.Load() != 1 || time.Since(start) > 2*time.Second {
		t.Errorf("err=%v calls=%d took=%v", err, calls.Load(), time.Since(start))
	}
}

func TestTemperatureSentWhenSet(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],"usage":{}}`))
	}))
	defer srv.Close()
	zero := 0.0
	if _, err := New("k", srv.URL, "v", "m", 100, 5, 1).Prompt(context.Background(), dto.PromptRequest{Temperature: &zero,
		Messages: []dto.Message{{Role: dto.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if v, has := got["temperature"]; !has || v != 0.0 {
		t.Errorf("temperature = %v %v", v, has)
	}
}
