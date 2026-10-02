// Package voyage is the Voyage AI embeddings client (D-082).
package voyage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type voyage struct {
	client  interfaces.ApiClient
	apiURL  string
	key     string
	model   string
	dims    int
	timeout time.Duration
}

var _ interfaces.Embedder = (*voyage)(nil)

// New returns a Voyage embedder. dims > 0 requests that output dimension;
// timeout bounds each call on top of the shared client's.
func New(client interfaces.ApiClient, apiURL, key, model string, dims int, timeout time.Duration) interfaces.Embedder {
	return &voyage{client: client, apiURL: apiURL, key: key, model: model, dims: dims, timeout: timeout}
}

type request struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension,omitempty"`
}

type response struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Detail string `json:"detail"`
}

func (v *voyage) Model() string { return v.model }

func (v *voyage) Embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if v.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}
	body, status, err := v.client.Post(ctx, v.apiURL, request{Input: texts, Model: v.model, InputType: inputType, OutputDimension: v.dims},
		map[string]string{"Authorization": "Bearer " + v.key, "Content-Type": "application/json"})
	if err != nil {
		return nil, fmt.Errorf("voyage: %w", err)
	}
	var resp response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("voyage: http %d: decode: %w", status, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("voyage: http %d: %s", status, resp.Detail)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("voyage: got %d embeddings for %d texts", len(resp.Data), len(texts))
	}
	sort.Slice(resp.Data, func(i, j int) bool { return resp.Data[i].Index < resp.Data[j].Index })
	out := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
