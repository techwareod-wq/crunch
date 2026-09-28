package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/utils"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (s *seoBlogGeneratorSiteIntelligence) checkCompletionAndDispatchPostProcessing(ctx context.Context, currentStep pipeline.ProcessType, userId, webEntityId, webEntityContextId string) error {
	status, err := models.GetSIEDataFetchStepStatus(ctx, webEntityContextId)
	if err != nil {
		return fmt.Errorf("failed to get SIE status: %w", err)
	}

	if !status.AllDone() {
		return nil
	}

	return s.pipeline.DispatchNext(ctx, currentStep, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        webEntityId,
		WebEntityContextID: webEntityContextId,
	})
}

// resolveDomainRating returns the web entity's stored user domain rating,
// falling back to the configured default when it is unset (0). The DR feeds the
// udr-relative keyword-difficulty band used by the ranked_keywords and
// keyword_ideas calls. The rating is populated during onboarding (Step 3,
// GetCompetitorInfo, via commonutils.FetchDomainRating) before SIE runs; the
// default only applies to entities onboarded before that fetch existed or when
// onboarding's best-effort fetch returned nothing.
func (s *seoBlogGeneratorSiteIntelligence) resolveDomainRating(webEntity *models.WebEntity) int {
	if webEntity.BusinessContext != nil && webEntity.BusinessContext.UserDomainRating > 0 {
		return webEntity.BusinessContext.UserDomainRating
	}
	return s.values.DefaultDomainRating
}

func (s *seoBlogGeneratorSiteIntelligence) requestKeywordData(ctx context.Context, url string, limit int, locationCode int, udr int, strategy string) ([]models.Keyword, error) {
	task := utils.GetkeywordRequestObject(limit, commonutils.CleanURL(url), locationCode, udr, s.values.FiltersForStrategy(strategy))
	req := dto.GetKeywordDataRequest{
		Tasks: []dto.GetKeywordDataTask{task},
	}

	resp, err := s.dataForSEO.GetKeywordData(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get keyword data for %s: %w", task.Target, err)
	}

	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 || len(resp.Tasks[0].Result[0].Items) == 0 {
		return nil, fmt.Errorf("no keyword data found for %s", task.Target)
	}

	var keywords []models.Keyword
	for _, item := range resp.Tasks[0].Result[0].Items {
		keyword := models.Keyword{
			Keyword:         item.KeywordData.Keyword,
			Description:     item.RankedSerpElement.SerpItem.Description,
			Volume:          item.KeywordData.KeywordInfo.SearchVolume,
			CPC:             item.KeywordData.KeywordInfo.CPC,
			RankingPosition: item.RankedSerpElement.SerpItem.RankAbsolute,
			RankingUrl:      item.RankedSerpElement.SerpItem.URL,
		}

		if item.KeywordData.KeywordProperties != nil && item.KeywordData.KeywordProperties.KeywordDifficulty != nil {
			keyword.KeywordDifficulty = *item.KeywordData.KeywordProperties.KeywordDifficulty
		}

		if item.KeywordData.SearchIntentInfo != nil {
			keyword.Intent = item.KeywordData.SearchIntentInfo.MainIntent
		}

		keywords = append(keywords, keyword)
	}

	return keywords, nil
}

func (s *seoBlogGeneratorSiteIntelligence) requestExpandedKeywordsData(ctx context.Context, preExpandedKeywords []string, limit int, locationCode int, udr int, strategy string) ([]models.Keyword, error) {
	task := utils.GetKeywordIdeasRequestObject(limit, preExpandedKeywords, locationCode, udr, s.values.FiltersForStrategy(strategy))
	req := dto.GetKeywordDataRequest{
		Tasks: []dto.GetKeywordDataTask{task},
	}

	resp, err := s.dataForSEO.GetKeywordIdeas(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get keyword data for %s: %w", task.Target, err)
	}

	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 || len(resp.Tasks[0].Result[0].Items) == 0 {
		return nil, fmt.Errorf("no keyword data found for %s", task.Target)
	}

	var keywords []models.Keyword
	for _, item := range resp.Tasks[0].Result[0].Items {
		keyword := models.Keyword{
			Keyword: item.Keyword,
			Volume:  item.KeywordInfo.SearchVolume,
			CPC:     item.KeywordInfo.CPC,
		}

		if item.KeywordProperties != nil && item.KeywordProperties.KeywordDifficulty != nil {
			keyword.KeywordDifficulty = *item.KeywordProperties.KeywordDifficulty
		}

		if item.SearchIntentInfo != nil {
			keyword.Intent = item.SearchIntentInfo.MainIntent
		}

		keywords = append(keywords, keyword)
	}

	return keywords, nil
}

// buildClusterSummaries renders existing clusters into the compact table form
// the cluster-assignment and re-clustering prompts consume: the pillar keyword
// text plus a capped sample of supporting keyword texts per cluster.
// keywordTextByID maps keyword ObjectIDs to their text; IDs absent from the map
// (e.g. a deleted keyword) are skipped. examplesPerCluster caps the supporting
// sample size.
func buildClusterSummaries(clusters []models.Cluster, keywordTextByID map[primitive.ObjectID]string, examplesPerCluster int) []sieDto.ClusterSummaryForPrompt {
	summaries := make([]sieDto.ClusterSummaryForPrompt, 0, len(clusters))
	for _, c := range clusters {
		examples := make([]string, 0, examplesPerCluster)
		for _, id := range c.SupportingKeywordIDs {
			if len(examples) >= examplesPerCluster {
				break
			}
			if text, ok := keywordTextByID[id]; ok && text != "" {
				examples = append(examples, text)
			}
		}
		summaries = append(summaries, sieDto.ClusterSummaryForPrompt{
			ClusterID:     c.ClusterID,
			ClusterName:   c.ClusterName,
			PillarKeyword: keywordTextByID[c.PillarKeywordID],
			Examples:      strings.Join(examples, ", "),
		})
	}
	return summaries
}

// formatCompetitorNames renders the discovered competitors into the
// comma-joined list the keyword-expansion prompt consumes for its
// competitor-anchored seeds, preferring the company name and falling back to the
// bare domain. Returns "" when there are no competitors so the prompt's
// "if empty, skip competitor terms" branch fires.
func formatCompetitorNames(competitors []models.Competitor) string {
	names := make([]string, 0, len(competitors))
	for _, c := range competitors {
		name := strings.TrimSpace(c.CompanyName)
		if name == "" {
			name = strings.TrimSpace(c.Domain)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

func mergeKeywords(kw models.KeyWords) []models.Keyword {
	var merged []models.Keyword
	merged = append(merged, kw.UserKeyWords...)
	merged = append(merged, kw.CompetitorKeyWords...)
	merged = append(merged, kw.ExpandedKeyWords...)
	return merged
}

func applyProcessingSteps(keywords []models.Keyword, processingSteps []keywordProcessingSteps) []models.Keyword {
	for _, f := range processingSteps {
		keywords = f(keywords)
	}
	return keywords
}
