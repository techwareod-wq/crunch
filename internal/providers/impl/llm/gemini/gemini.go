package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/atharva-ng/crunch/internal/dto"
)

type geminiService struct {
	apiKey        string
	apiURL        string
	fallbackModel string
	client        *http.Client
}

func New(apiKey, apiURL, fallbackModel string) *geminiService {
	return &geminiService{
		apiKey:        apiKey,
		apiURL:        apiURL,
		fallbackModel: fallbackModel,
		client:        http.DefaultClient,
	}
}

type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type generateRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	ModelVersion  string `json:"modelVersion"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *geminiService) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	model := req.Model
	if model == "" {
		model = s.fallbackModel
	}

	var systemContent *content
	var contents []content
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemContent = &content{
				Role:  "user",
				Parts: []part{{Text: m.Content}},
			}
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, content{
			Role:  role,
			Parts: []part{{Text: m.Content}},
		})
	}

	body := generateRequest{
		Contents:          contents,
		SystemInstruction: systemContent,
	}

	if req.MaxTokens > 0 || req.Temperature > 0 {
		gc := &generationConfig{}
		if req.MaxTokens > 0 {
			gc.MaxOutputTokens = req.MaxTokens
		}
		if req.Temperature > 0 {
			gc.Temperature = &req.Temperature
		}
		body.GenerationConfig = gc
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini: failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s", s.apiURL, model, s.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gemini: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("gemini: API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
	}

	var genResp generateResponse
	if err := json.Unmarshal(respBody, &genResp); err != nil {
		return nil, fmt.Errorf("gemini: failed to parse response: %w", err)
	}

	if len(genResp.Candidates) == 0 || len(genResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini: no content returned")
	}

	return &dto.PromptResponse{
		Content:      genResp.Candidates[0].Content.Parts[0].Text,
		Model:        genResp.ModelVersion,
		Provider:     "gemini",
		PromptTokens: genResp.UsageMetadata.PromptTokenCount,
		OutputTokens: genResp.UsageMetadata.CandidatesTokenCount,
	}, nil
}
