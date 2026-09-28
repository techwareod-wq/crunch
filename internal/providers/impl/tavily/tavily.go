package tavily

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var _ interfaces.Tavily = (*tavilyProvider)(nil)

var (
	instance *tavilyProvider
	once     sync.Once
)

type tavilyProvider struct {
	apiClient interfaces.ApiClient
	apiKey    string
	searchURL string
}

func GetProvider(apiClient interfaces.ApiClient, apiKey, searchURL string) interfaces.Tavily {
	once.Do(func() {
		instance = &tavilyProvider{
			apiClient: apiClient,
			apiKey:    apiKey,
			searchURL: searchURL,
		}
	})
	return instance
}

func (t *tavilyProvider) Search(ctx context.Context, req dto.TavilySearchRequest) (*dto.TavilySearchResponse, error) {
	body := map[string]interface{}{
		"api_key":      t.apiKey,
		"query":        req.Query,
		"search_depth": req.SearchDepth,
		"max_results":  req.MaxResults,
	}

	headers := map[string]string{}

	respBody, statusCode, err := t.apiClient.Post(ctx, t.searchURL, body, headers)
	if err != nil {
		return nil, fmt.Errorf("tavily: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.TavilySearchResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("tavily: failed to unmarshal response: %w", err)
	}

	return &response, nil
}
