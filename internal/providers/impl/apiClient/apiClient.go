package apiClient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var _ interfaces.ApiClient = (*apiClient)(nil)

var (
	instance *apiClient
	once     sync.Once
)

type apiClient struct {
	client *http.Client
}

func GetClient(timeout time.Duration) interfaces.ApiClient {
	once.Do(func() {
		instance = &apiClient{
			client: &http.Client{
				Timeout: timeout,
				Transport: &http.Transport{
					TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
					TLSHandshakeTimeout: 15 * time.Second,
					DialContext: (&net.Dialer{
						Timeout: 10 * time.Second,
					}).DialContext,
					MaxIdleConns:        100,
					MaxIdleConnsPerHost: 10,
					IdleConnTimeout:     90 * time.Second,
				},
			},
		}
	})
	return instance
}

func (a *apiClient) Get(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	return a.doRequest(ctx, http.MethodGet, url, nil, headers)
}

func (a *apiClient) Post(ctx context.Context, url string, body any, headers map[string]string) ([]byte, int, error) {
	return a.doRequest(ctx, http.MethodPost, url, body, headers)
}

func (a *apiClient) Put(ctx context.Context, url string, body any, headers map[string]string) ([]byte, int, error) {
	return a.doRequest(ctx, http.MethodPut, url, body, headers)
}

func (a *apiClient) Delete(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	return a.doRequest(ctx, http.MethodDelete, url, nil, headers)
}

func (a *apiClient) doRequest(ctx context.Context, method string, url string, body any, headers map[string]string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("apiClient: failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("apiClient: failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("apiClient: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("apiClient: failed to read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}
