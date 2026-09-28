package dataForSEO

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var _ interfaces.DataForSEO = (*dataForSEOProvider)(nil)

const dataForSEOBaseURL = "https://api.dataforseo.com"

const versionV3 = "v3"

const (
	rankedKeywordsEndpoint         = "dataforseo_labs/google/ranked_keywords/live"
	keywordIdeasEndpoint           = "dataforseo_labs/google/keyword_ideas/live"
	serpOrganicEndpoint            = "serp/google/organic/live/regular"
	serpOrganicAdvancedEndpoint    = "serp/google/organic/live/advanced"
	keywordOverviewEndpoint        = "dataforseo_labs/google/keyword_overview/live"
	keywordSuggestionsEndpoint     = "dataforseo_labs/google/keyword_suggestions/live"
	relatedKeywordsEndpoint        = "dataforseo_labs/google/related_keywords/live"
	locationsEndpoint              = "dataforseo_labs/locations_and_languages"
	backlinksSummaryEndpoint       = "backlinks/summary/live"
	onPageInstantPagesEndpoint     = "on_page/instant_pages"
	onPageRawHtmlEndpoint          = "on_page/raw_html"
	onPageTaskPostEndpoint         = "on_page/task_post"
	onPageSummaryEndpoint          = "on_page/summary" // GET on_page/summary/{id}
	onPagePagesEndpoint            = "on_page/pages"
	onPageLinksEndpoint            = "on_page/links"
	onPageDuplicateContentEndpoint = "on_page/duplicate_content"
	domainRankOverviewEndpoint     = "dataforseo_labs/google/domain_rank_overview/live"
)

// resolveURL builds a full DataForSEO request URL from an API version and endpoint path.
func resolveURL(version, endpoint string) string {
	return fmt.Sprintf("%s/%s/%s", dataForSEOBaseURL, version, endpoint)
}

var (
	instance *dataForSEOProvider
	once     sync.Once
)

type dataForSEOProvider struct {
	apiClient interfaces.ApiClient
	authToken string
}

func GetProvider(apiClient interfaces.ApiClient, username, password string) interfaces.DataForSEO {
	once.Do(func() {
		token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		instance = &dataForSEOProvider{
			apiClient: apiClient,
			authToken: token,
		}
	})
	return instance
}

func (d *dataForSEOProvider) GetKeywordData(ctx context.Context, req dto.GetKeywordDataRequest) (*dto.RankedKeywordsResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, rankedKeywordsEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.RankedKeywordsResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetKeywordIdeas(ctx context.Context, req dto.GetKeywordDataRequest) (*dto.KeywordIdeasResponse, error) {
	body := req.Tasks

	if body[0].Keywords == nil {
		return nil, fmt.Errorf("dataforseo: keywords field is required for keyword ideas")
	}

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, keywordIdeasEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.KeywordIdeasResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetKeywordOverview(ctx context.Context, req dto.GetKeywordOverviewRequest) (*dto.KeywordOverviewResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, keywordOverviewEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.KeywordOverviewResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetKeywordSuggestions(ctx context.Context, req dto.KeywordSuggestionsRequest) (*dto.KeywordSuggestionsResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, keywordSuggestionsEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.KeywordSuggestionsResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetRelatedKeywords(ctx context.Context, req dto.RelatedKeywordsRequest) (*dto.RelatedKeywordsResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, relatedKeywordsEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.RelatedKeywordsResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetSerpResults(ctx context.Context, req dto.GetSerpResultsRequest) (*dto.SerpResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, serpOrganicEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.SerpResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetBacklinksSummary(ctx context.Context, req dto.BacklinksSummaryRequest) (*dto.BacklinksSummaryResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, backlinksSummaryEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.BacklinksSummaryResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetLocations(ctx context.Context) (*dto.LocationsResponse, error) {
	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Get(ctx, resolveURL(versionV3, locationsEndpoint), headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.LocationsResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetInstantPages(ctx context.Context, req dto.InstantPagesRequest) (*dto.InstantPagesResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPageInstantPagesEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.InstantPagesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetRawHtml(ctx context.Context, req dto.RawHtmlRequest) (*dto.RawHtmlResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPageRawHtmlEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.RawHtmlResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) PostOnPageTask(ctx context.Context, req dto.OnPageTaskPostRequest) (*dto.OnPageTaskPostResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPageTaskPostEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.OnPageTaskPostResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

// GetOnPageSummary is the ONE GET in the OnPage task flow — the crawl-status
// poll target (GET on_page/summary/{id}).
func (d *dataForSEOProvider) GetOnPageSummary(ctx context.Context, taskID string) (*dto.OnPageSummaryResponse, error) {
	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Get(ctx, resolveURL(versionV3, onPageSummaryEndpoint)+"/"+taskID, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.OnPageSummaryResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetOnPagePages(ctx context.Context, req dto.OnPagePagesRequest) (*dto.OnPagePagesResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPagePagesEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.OnPagePagesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetOnPageLinks(ctx context.Context, req dto.OnPageLinksRequest) (*dto.OnPageLinksResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPageLinksEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.OnPageLinksResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetOnPageDuplicateContent(ctx context.Context, req dto.OnPageDuplicateContentRequest) (*dto.OnPageDuplicateContentResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, onPageDuplicateContentEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.OnPageDuplicateContentResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetDomainRankOverview(ctx context.Context, req dto.DomainRankOverviewRequest) (*dto.DomainRankOverviewResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, domainRankOverviewEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.DomainRankOverviewResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (d *dataForSEOProvider) GetAdvancedSerpResults(ctx context.Context, req dto.GetAdvancedSerpRequest) (*dto.AdvancedSerpResponse, error) {
	body := req.Tasks

	headers := map[string]string{
		"Authorization": "Basic " + d.authToken,
	}

	respBody, statusCode, err := d.apiClient.Post(ctx, resolveURL(versionV3, serpOrganicAdvancedEndpoint), body, headers)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("dataforseo: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var response dto.AdvancedSerpResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("dataforseo: failed to unmarshal response: %w", err)
	}

	return &response, nil
}
