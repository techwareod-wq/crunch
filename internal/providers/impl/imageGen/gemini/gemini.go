package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type geminiImageProvider struct {
	apiKey             string
	apiURL             string
	defaultAspectRatio string
	client             *http.Client
}

// New constructs the Gemini image provider. requestTimeout caps a single image
// generation call — Gemini image generation is slow, so this is deliberately
// generous (see values.apis.imageGen.gemini.requestTimeoutSeconds).
func New(apiKey, apiURL, defaultAspectRatio string, requestTimeout time.Duration) interfaces.ImageGenerator {
	return &geminiImageProvider{
		apiKey:             apiKey,
		apiURL:             apiURL,
		defaultAspectRatio: defaultAspectRatio,
		client:             &http.Client{Timeout: requestTimeout},
	}
}

type part struct {
	Text string `json:"text"`
}

type content struct {
	Parts []part `json:"parts"`
}

type imageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
}

type generationConfig struct {
	ResponseModalities []string     `json:"responseModalities"`
	ImageConfig        *imageConfig `json:"imageConfig,omitempty"`
}

type generateRequest struct {
	Contents         []content        `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig"`
}

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData *inlineData `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *geminiImageProvider) GenerateImage(ctx context.Context, req interfaces.ImageGenRequest) (*interfaces.ImageGenResponse, error) {
	aspectRatio := req.AspectRatio
	if aspectRatio == "" {
		aspectRatio = p.defaultAspectRatio
	}

	body := generateRequest{
		Contents: []content{{Parts: []part{{Text: req.Prompt}}}},
		GenerationConfig: generationConfig{
			ResponseModalities: []string{"IMAGE"},
			ImageConfig:        &imageConfig{AspectRatio: aspectRatio},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini image: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gemini image: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-goog-api-key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini image: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini image: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("gemini image: API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
	}

	var genResp generateResponse
	if err := json.Unmarshal(respBody, &genResp); err != nil {
		return nil, fmt.Errorf("gemini image: failed to parse response: %w", err)
	}

	for _, c := range genResp.Candidates {
		for _, prt := range c.Content.Parts {
			if prt.InlineData == nil || prt.InlineData.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(prt.InlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("gemini image: failed to decode image data: %w", err)
			}
			return &interfaces.ImageGenResponse{
				Data:     data,
				MimeType: prt.InlineData.MimeType,
			}, nil
		}
	}

	return nil, fmt.Errorf("gemini image: no image returned")
}
