package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type openaiImageProvider struct {
	apiKey             string
	apiURL             string
	model              string
	defaultAspectRatio string
	client             *http.Client
}

// New constructs the OpenAI image provider (gpt-image-2). requestTimeout caps a
// single image generation call — image generation is slow, so this is
// deliberately generous (see values.apis.imageGen.openai.requestTimeoutSeconds).
func New(apiKey, apiURL, model, defaultAspectRatio string, requestTimeout time.Duration) interfaces.ImageGenerator {
	return &openaiImageProvider{
		apiKey:             apiKey,
		apiURL:             apiURL,
		model:              model,
		defaultAspectRatio: defaultAspectRatio,
		client:             &http.Client{Timeout: requestTimeout},
	}
}

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size"`
	N      int    `json:"n"`
}

type generateResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *openaiImageProvider) GenerateImage(ctx context.Context, req interfaces.ImageGenRequest) (*interfaces.ImageGenResponse, error) {
	aspectRatio := req.AspectRatio
	if aspectRatio == "" {
		aspectRatio = p.defaultAspectRatio
	}

	body := generateRequest{
		Model:  p.model,
		Prompt: req.Prompt,
		Size:   aspectRatioToSize(aspectRatio),
		N:      1,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai image: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai image: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai image: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai image: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("openai image: API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
	}

	var genResp generateResponse
	if err := json.Unmarshal(respBody, &genResp); err != nil {
		return nil, fmt.Errorf("openai image: failed to parse response: %w", err)
	}

	for _, d := range genResp.Data {
		if d.B64JSON == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("openai image: failed to decode image data: %w", err)
		}
		// gpt-image-2 returns PNG by default.
		return &interfaces.ImageGenResponse{
			Data:     data,
			MimeType: "image/png",
		}, nil
	}

	return nil, fmt.Errorf("openai image: no image returned")
}

// aspectRatioToSize maps an aspect ratio (e.g. "16:9") to the nearest gpt-image
// supported size. gpt-image only accepts a fixed set of sizes — square,
// landscape, and portrait — so any wide ratio collapses to landscape and any
// tall ratio to portrait. Unparseable input falls back to landscape.
func aspectRatioToSize(aspectRatio string) string {
	const (
		square    = "1024x1024"
		landscape = "1536x1024"
		portrait  = "1024x1536"
	)

	parts := strings.Split(aspectRatio, ":")
	if len(parts) != 2 {
		return landscape
	}
	w, errW := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	h, errH := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return landscape
	}

	switch {
	case w > h:
		return landscape
	case h > w:
		return portrait
	default:
		return square
	}
}
