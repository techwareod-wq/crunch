// Package psi implements the Google PageSpeed Insights provider (the
// runPagespeed GET; endpoint URL from values apis.psi.runPagespeedURL). One
// mobile call per audit on the homepage; CrUX origin field data is the
// Performance scoring input (decision 6).
package psi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var _ interfaces.PageSpeedInsights = (*psiProvider)(nil)

var (
	instance *psiProvider
	once     sync.Once
)

type psiProvider struct {
	apiClient       interfaces.ApiClient
	apiKey          string
	runPagespeedURL string
}

// GetProvider returns the process-wide PSI provider (sync.Once singleton,
// the tavily shape: endpoint URL from values apis.psi.runPagespeedURL). An
// empty apiKey is tolerated at construction — the call fails loudly instead,
// and the audit's PSI collector degrades that to a constraint (§8.3).
func GetProvider(apiClient interfaces.ApiClient, apiKey, runPagespeedURL string) interfaces.PageSpeedInsights {
	once.Do(func() {
		instance = &psiProvider{
			apiClient:       apiClient,
			apiKey:          apiKey,
			runPagespeedURL: runPagespeedURL,
		}
	})
	return instance
}

func (p *psiProvider) RunPagespeed(ctx context.Context, req dto.PagespeedRequest) (*dto.PagespeedResponse, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("psi: GOOGLE_PSI_API_KEY is not configured")
	}

	q := url.Values{}
	q.Set("url", req.URL)
	q.Set("key", p.apiKey)
	q.Set("category", "performance")
	if req.Strategy != "" {
		q.Set("strategy", req.Strategy)
	}

	respBody, statusCode, err := p.apiClient.Get(ctx, p.runPagespeedURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("psi: request failed: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("psi: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.PagespeedResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("psi: failed to unmarshal response: %w", err)
	}
	return &response, nil
}
