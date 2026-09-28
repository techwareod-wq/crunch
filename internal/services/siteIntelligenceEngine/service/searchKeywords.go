package service

import (
	"context"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// defaultKeywordSearchLimit caps search results when
// values.siteIntelligence.keywordSearchLimit is missing or non-positive.
const defaultKeywordSearchLimit = 50

// SearchKeywords matches a web entity's keywords by text across every cluster.
// It exists because the keyword-data payload embeds only the first page of each
// cluster's supporting keywords: a client filtering that payload in memory can
// only ever search a fraction of the keyword set. query matches the keyword text
// or its cluster name, case-insensitively; an empty query matches everything.
// status, when set, keeps only keywords with that wire status. usage filters on
// the derived used flag: "used" keeps keywords that already carry ≥1 article,
// "unused" keeps article-free ones, ""/"all" keeps everything (the article
// picker's default — used keywords render tagged, not hidden). Results are
// capped at Limit — Total reports how many matched before the cap.
//
// Only clustered keywords are searchable. A manually-added keyword that is still
// enriching belongs to no cluster yet, so it is absent here for the same reason
// it is absent from the keyword-data payload.
func (s *seoBlogGeneratorSiteIntelligence) SearchKeywords(ctx context.Context, userId, webEntityId, query, status, usage string) (*dto.KeywordSearchResponse, error) {
	wec, err := s.getCompletedWebEntityContext(ctx, userId, webEntityId)
	if err != nil {
		return nil, err
	}

	lookups, err := s.loadKeywordLookups(ctx, wec.ID.Hex())
	if err != nil {
		return nil, err
	}

	return buildKeywordSearchResponse(wec, lookups, query, status, usage, s.values.KeywordSearchLimit), nil
}

// buildKeywordSearchResponse is the matching, ranking, and capping half of
// SearchKeywords, split out from the DB half so it can be tested directly —
// same split as buildKeywordDataResponse. A non-positive limit falls back to
// defaultKeywordSearchLimit.
func buildKeywordSearchResponse(
	wec *models.WebEntityContext,
	lookups *keywordLookups,
	query, status, usage string,
	limit int,
) *dto.KeywordSearchResponse {
	if limit <= 0 {
		limit = defaultKeywordSearchLimit
	}

	needle := strings.ToLower(strings.TrimSpace(query))

	matches := make([]dto.KeywordSearchResultDTO, 0, limit)
	for i, c := range wec.Clusters {
		// A cluster-name hit admits every keyword in the cluster — same rule the
		// picker's old in-memory filter used.
		clusterHit := needle != "" && strings.Contains(strings.ToLower(c.ClusterName), needle)

		ids := make([]primitive.ObjectID, 0, 1+len(c.SupportingKeywordIDs))
		ids = append(ids, c.PillarKeywordID)
		ids = append(ids, c.SupportingKeywordIDs...)

		for _, kwID := range ids {
			kw, ok := lookups.keywordByID[kwID]
			if !ok {
				continue
			}
			if needle != "" && !clusterHit && !strings.Contains(strings.ToLower(kw.Keyword), needle) {
				continue
			}

			keyword := toKeywordDTO(kw, lookups.articleByKeyword[kwID], lookups.articleCountByKeyword[kwID], lookups.erroredArticles)
			if status != "" && keyword.Status != status {
				continue
			}
			if (usage == dto.KeywordUsageUsed && !keyword.Used) ||
				(usage == dto.KeywordUsageUnused && keyword.Used) {
				continue
			}

			matches = append(matches, dto.KeywordSearchResultDTO{
				KeywordDTO:   keyword,
				Cluster:      c.ClusterName,
				ClusterIndex: i,
			})
		}
	}

	// Highest opportunity first, so truncating to the limit keeps the keywords
	// worth writing about. Keyword text breaks ties to keep the order stable
	// across identical queries.
	sort.SliceStable(matches, func(a, b int) bool {
		if matches[a].Score != matches[b].Score {
			return matches[a].Score > matches[b].Score
		}
		return matches[a].Keyword < matches[b].Keyword
	})

	total := len(matches)
	if total > limit {
		matches = matches[:limit]
	}

	return &dto.KeywordSearchResponse{
		Query:    query,
		Status:   status,
		Usage:    usage,
		Total:    total,
		Limit:    limit,
		Keywords: matches,
	}
}
