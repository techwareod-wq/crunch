package utils

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/dto"
)

// fakeDataForSEO implements interfaces.DataForSEO for FetchDomainRating tests.
// Only GetBacklinksSummary is exercised; the rest satisfy the interface and
// panic if unexpectedly called.
type fakeDataForSEO struct {
	resp *dto.BacklinksSummaryResponse
	err  error
	// gotTarget captures the target sent to GetBacklinksSummary so the test can
	// assert the host was extracted from the raw URL.
	gotTarget string

	// OnPage fixtures for FetchRenderedWebsiteContent tests. Unconfigured
	// methods keep the panic-on-unexpected-call behaviour. instantSeq, when
	// set, serves responses per call in order (for retry tests), overriding
	// instantResp; calls beyond the sequence reuse the last entry.
	instantResp  *dto.InstantPagesResponse
	instantSeq   []*dto.InstantPagesResponse
	instantCalls int
	instantErr   error
	rawResp      *dto.RawHtmlResponse
	rawErr       error
}

func (f *fakeDataForSEO) GetBacklinksSummary(_ context.Context, req dto.BacklinksSummaryRequest) (*dto.BacklinksSummaryResponse, error) {
	if len(req.Tasks) > 0 {
		f.gotTarget = req.Tasks[0].Target
	}
	return f.resp, f.err
}

func (f *fakeDataForSEO) GetKeywordData(context.Context, dto.GetKeywordDataRequest) (*dto.RankedKeywordsResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetKeywordIdeas(context.Context, dto.GetKeywordDataRequest) (*dto.KeywordIdeasResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetKeywordOverview(context.Context, dto.GetKeywordOverviewRequest) (*dto.KeywordOverviewResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetKeywordSuggestions(context.Context, dto.KeywordSuggestionsRequest) (*dto.KeywordSuggestionsResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetRelatedKeywords(context.Context, dto.RelatedKeywordsRequest) (*dto.RelatedKeywordsResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetSerpResults(context.Context, dto.GetSerpResultsRequest) (*dto.SerpResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetAdvancedSerpResults(context.Context, dto.GetAdvancedSerpRequest) (*dto.AdvancedSerpResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetLocations(context.Context) (*dto.LocationsResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetInstantPages(context.Context, dto.InstantPagesRequest) (*dto.InstantPagesResponse, error) {
	f.instantCalls++
	if len(f.instantSeq) > 0 {
		i := f.instantCalls - 1
		if i >= len(f.instantSeq) {
			i = len(f.instantSeq) - 1
		}
		return f.instantSeq[i], nil
	}
	if f.instantResp == nil && f.instantErr == nil {
		panic("unexpected call")
	}
	return f.instantResp, f.instantErr
}
func (f *fakeDataForSEO) GetRawHtml(context.Context, dto.RawHtmlRequest) (*dto.RawHtmlResponse, error) {
	if f.rawResp == nil && f.rawErr == nil {
		panic("unexpected call")
	}
	return f.rawResp, f.rawErr
}
func (f *fakeDataForSEO) PostOnPageTask(context.Context, dto.OnPageTaskPostRequest) (*dto.OnPageTaskPostResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetOnPageSummary(context.Context, string) (*dto.OnPageSummaryResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetOnPagePages(context.Context, dto.OnPagePagesRequest) (*dto.OnPagePagesResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetOnPageLinks(context.Context, dto.OnPageLinksRequest) (*dto.OnPageLinksResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetOnPageDuplicateContent(context.Context, dto.OnPageDuplicateContentRequest) (*dto.OnPageDuplicateContentResponse, error) {
	panic("unexpected call")
}
func (f *fakeDataForSEO) GetDomainRankOverview(context.Context, dto.DomainRankOverviewRequest) (*dto.DomainRankOverviewResponse, error) {
	panic("unexpected call")
}

func backlinksResp(ranks ...int) *dto.BacklinksSummaryResponse {
	results := make([]dto.BacklinksSummaryResult, 0, len(ranks))
	for _, r := range ranks {
		results = append(results, dto.BacklinksSummaryResult{Rank: r})
	}
	return &dto.BacklinksSummaryResponse{
		Tasks: []dto.BacklinksSummaryRespTask{{Result: results}},
	}
}

func TestFetchDomainRating(t *testing.T) {
	tests := []struct {
		name     string
		resp     *dto.BacklinksSummaryResponse
		err      error
		wantRank int
		wantErr  bool
	}{
		{
			name:     "happy path returns result rank",
			resp:     backlinksResp(55),
			wantRank: 55,
		},
		{
			name:     "zero rank surfaces as unknown (0, no error)",
			resp:     backlinksResp(0),
			wantRank: 0,
		},
		{
			name:    "provider error",
			err:     errors.New("dataforseo down"),
			wantErr: true,
		},
		{
			name:    "empty tasks",
			resp:    &dto.BacklinksSummaryResponse{Tasks: nil},
			wantErr: true,
		},
		{
			name:    "empty result",
			resp:    &dto.BacklinksSummaryResponse{Tasks: []dto.BacklinksSummaryRespTask{{Result: nil}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDataForSEO{resp: tt.resp, err: tt.err}
			got, err := FetchDomainRating(context.Background(), fake, "https://www.example.com/blog/")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got rank %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantRank {
				t.Errorf("rank = %d, want %d", got, tt.wantRank)
			}
		})
	}
}

// FetchDomainRating must key the backlinks lookup on the bare host, matching
// ExtractDomain — scheme, www., path and trailing slash all stripped — or
// DataForSEO's domain-target lookup is narrowed.
func TestFetchDomainRatingExtractsHost(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/blog/": "example.com",
		"http://Sub.Example.com:8080/x": "sub.example.com",
		"PQR.ai/":                       "pqr.ai",
		"user:pass@abc.com/path":        "abc.com",
	}
	for rawURL, wantHost := range cases {
		fake := &fakeDataForSEO{resp: backlinksResp(10)}
		if _, err := FetchDomainRating(context.Background(), fake, rawURL); err != nil {
			t.Fatalf("FetchDomainRating(%q) unexpected error: %v", rawURL, err)
		}
		if fake.gotTarget != wantHost {
			t.Errorf("target for %q = %q, want %q", rawURL, fake.gotTarget, wantHost)
		}
	}
}
