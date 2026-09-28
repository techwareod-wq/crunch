package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/atharva-ng/crunch/internal/dto"
)

type openaiService struct {
	apiKey        string
	apiURL        string
	fallbackModel string
	client        *http.Client
}

func New(apiKey, apiURL, fallbackModel string) *openaiService {
	return &openaiService{
		apiKey:        apiKey,
		apiURL:        apiURL,
		fallbackModel: fallbackModel,
		client:        http.DefaultClient,
	}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *openaiService) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	model := req.Model
	if model == "" {
		model = s.fallbackModel
	}

	msgs := make([]chatMsg, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = chatMsg{Role: m.Role, Content: m.Content}
	}

	body := chatRequest{
		Model:    model,
		Messages: msgs,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		body.Temperature = &req.Temperature
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("openai: API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("openai: failed to parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices returned")
	}

	return &dto.PromptResponse{
		Content:      chatResp.Choices[0].Message.Content,
		Model:        chatResp.Model,
		Provider:     "openai",
		PromptTokens: chatResp.Usage.PromptTokens,
		OutputTokens: chatResp.Usage.CompletionTokens,
	}, nil
}
