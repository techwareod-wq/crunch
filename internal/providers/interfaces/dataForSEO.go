package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

type DataForSEO interface {
	GetKeywordData(ctx context.Context, req dto.GetKeywordDataRequest) (*dto.RankedKeywordsResponse, error)
	GetKeywordIdeas(ctx context.Context, req dto.GetKeywordDataRequest) (*dto.KeywordIdeasResponse, error)
	GetKeywordOverview(ctx context.Context, req dto.GetKeywordOverviewRequest) (*dto.KeywordOverviewResponse, error)
	GetKeywordSuggestions(ctx context.Context, req dto.KeywordSuggestionsRequest) (*dto.KeywordSuggestionsResponse, error)
	GetRelatedKeywords(ctx context.Context, req dto.RelatedKeywordsRequest) (*dto.RelatedKeywordsResponse, error)
	GetSerpResults(ctx context.Context, req dto.GetSerpResultsRequest) (*dto.SerpResponse, error)
	GetAdvancedSerpResults(ctx context.Context, req dto.GetAdvancedSerpRequest) (*dto.AdvancedSerpResponse, error)
	GetBacklinksSummary(ctx context.Context, req dto.BacklinksSummaryRequest) (*dto.BacklinksSummaryResponse, error)
	GetLocations(ctx context.Context) (*dto.LocationsResponse, error)
	GetInstantPages(ctx context.Context, req dto.InstantPagesRequest) (*dto.InstantPagesResponse, error)
	GetRawHtml(ctx context.Context, req dto.RawHtmlRequest) (*dto.RawHtmlResponse, error)

	// OnPage task API (audit engine crawl): post a site crawl, poll its
	// summary, then page through results. GetOnPageSummary is the one GET.
	PostOnPageTask(ctx context.Context, req dto.OnPageTaskPostRequest) (*dto.OnPageTaskPostResponse, error)
	GetOnPageSummary(ctx context.Context, taskID string) (*dto.OnPageSummaryResponse, error)
	GetOnPagePages(ctx context.Context, req dto.OnPagePagesRequest) (*dto.OnPagePagesResponse, error)
	GetOnPageLinks(ctx context.Context, req dto.OnPageLinksRequest) (*dto.OnPageLinksResponse, error)
	GetOnPageDuplicateContent(ctx context.Context, req dto.OnPageDuplicateContentRequest) (*dto.OnPageDuplicateContentResponse, error)
	// GetDomainRankOverview is dataforseo_labs/google/domain_rank_overview/live
	// (audit Domain Overview strip, decision 7).
	GetDomainRankOverview(ctx context.Context, req dto.DomainRankOverviewRequest) (*dto.DomainRankOverviewResponse, error)
}
