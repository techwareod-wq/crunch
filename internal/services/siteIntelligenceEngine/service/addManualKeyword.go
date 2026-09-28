package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/prompts"
	"github.com/atharva-ng/crunch/internal/util/log"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Wire-level labels for the fallback endpoint that produced a suggestion. These
// are part of the API contract (not tunable), so they stay as constants; the
// numeric fallback knobs live in config (s.values.Manual).
const (
	manualSuggestionSourceKeywordSuggestions = "keyword_suggestions"
	manualSuggestionSourceRelatedKeywords    = "related_keywords"
)

func (s *seoBlogGeneratorSiteIntelligence) AddManualKeyword(ctx context.Context, userId string, req sieDto.AddManualKeywordRequest) (*sieDto.AddManualKeywordResponse, error) {
	keyword := strings.TrimSpace(req.Keyword)
	if keyword == "" {
		return nil, ErrManualKeywordNoData
	}

	found, wec, err := models.GetWebEntityContextFromWebEntityAndUserID(ctx, req.WebEntityID, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !found {
		return nil, ErrWebEntityContextNotFound
	}
	// Live append requires an already-clustered WEC. EffectiveStatus covers the
	// case where clustering finished but a later stage errored (Status ==
	// SIEStatusError).
	if wec.EffectiveStatus() < models.SIEStatusClusteringDone {
		return nil, ErrPipelineNotReady
	}

	dup, _, err := models.GetKeywordByWECAndText(ctx, wec.ID.Hex(), keyword)
	if err != nil {
		return nil, fmt.Errorf("failed duplicate check: %w", err)
	}
	if dup {
		return nil, ErrManualKeywordDuplicate
	}

	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, wec.WebEntityID.Hex())
	if err != nil {
		return nil, fmt.Errorf("failed to find web entity: %w", err)
	}
	if !isWebEntity {
		return nil, fmt.Errorf("web entity not found: %s", wec.WebEntityID.Hex())
	}

	kwData, err := s.fetchManualKeywordData(ctx, keyword, webEntity.LocationCode)
	if errors.Is(err, ErrManualKeywordNoData) {
		// The exact keyword has no reliable data. Instead of failing outright,
		// offer related alternatives the user can look up. Nothing is inserted
		// and no enrichment is dispatched for a suggestions response.
		suggestions, sugErr := s.fetchManualKeywordSuggestions(ctx, keyword, webEntity.LocationCode)
		if sugErr != nil {
			return nil, err // keep original no-data behaviour if the fallback also fails
		}
		if len(suggestions) > 0 {
			return &sieDto.AddManualKeywordResponse{
				Status:      "suggestions",
				Message:     "No reliable data found for this keyword. Try one of these related keywords instead.",
				Suggestions: suggestions,
			}, nil
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	lowVolume := kwData.Volume == 0

	seqID, err := models.GetMaxSequenceIDForWEC(ctx, wec.ID.Hex())
	if err != nil {
		return nil, fmt.Errorf("failed to get max sequence id: %w", err)
	}

	stub := models.Keyword{
		SequenceID:        seqID + 1,
		Keyword:           kwData.Keyword,
		Volume:            kwData.Volume,
		CPC:               kwData.CPC,
		KeywordDifficulty: kwData.KeywordDifficulty,
		Intent:            kwData.Intent,
		ManuallyAdded:     true,
		Completed:         false,
	}

	inserted, err := models.InsertKeywords(ctx, wec.ID.Hex(), []models.Keyword{stub})
	if err != nil {
		return nil, fmt.Errorf("failed to insert manual keyword: %w", err)
	}
	stored := inserted[0]

	if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEAddManualKeyword), userId, sie.ManualKeywordPayload{
		WebEntityContextID: wec.ID.Hex(),
		KeywordID:          stored.ID,
	}); err != nil {
		return nil, fmt.Errorf("failed to dispatch manual keyword enrichment: %w", err)
	}

	kwDTO := toKeywordDTO(stored, nil, 0, nil)
	return &sieDto.AddManualKeywordResponse{
		Keyword:   &kwDTO,
		LowVolume: lowVolume,
		Status:    "enriching",
	}, nil
}

func (s *seoBlogGeneratorSiteIntelligence) fetchManualKeywordData(ctx context.Context, keyword string, locationCode int) (*models.Keyword, error) {
	req := dto.GetKeywordOverviewRequest{
		Tasks: []dto.KeywordOverviewTask{{
			Keywords:     []string{keyword},
			LanguageName: "English",
			LocationCode: locationCode,
		}},
	}

	resp, err := s.dataForSEO.GetKeywordOverview(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to look up keyword data for %q: %w", keyword, err)
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 || len(resp.Tasks[0].Result[0].Items) == 0 {
		return nil, ErrManualKeywordNoData
	}

	item := resp.Tasks[0].Result[0].Items[0]
	kw := &models.Keyword{
		Keyword: item.Keyword,
		Volume:  item.KeywordInfo.SearchVolume,
		CPC:     item.KeywordInfo.CPC,
	}
	if item.KeywordProperties != nil && item.KeywordProperties.KeywordDifficulty != nil {
		kw.KeywordDifficulty = *item.KeywordProperties.KeywordDifficulty
	}
	if item.SearchIntentInfo != nil {
		kw.Intent = item.SearchIntentInfo.MainIntent
	}
	return kw, nil
}

// fetchManualKeywordSuggestions is the no-data fallback: it tries the keyword
// suggestions endpoint first (closest to what the user typed) and only reaches
// for the broader related-keywords discovery when suggestions come back empty
// or too thin. The merged set is filtered, deduped, sorted and capped before
// being returned. A nil error with an empty slice means "no usable suggestions".
func (s *seoBlogGeneratorSiteIntelligence) fetchManualKeywordSuggestions(ctx context.Context, keyword string, locationCode int) ([]sieDto.ManualKeywordSuggestion, error) {
	suggestions, err := s.requestKeywordSuggestions(ctx, keyword, locationCode)
	if err != nil {
		return nil, err
	}

	if len(suggestions) < s.values.Manual.SuggestionsMinResults {
		related, relErr := s.requestRelatedKeywords(ctx, keyword, locationCode)
		if relErr != nil {
			// keyword_suggestions already gave us whatever it had; a
			// related-keywords failure shouldn't sink the whole fallback.
			log.Warn("manual keyword: related keywords fallback failed", "keyword", keyword, "error", relErr)
		} else {
			suggestions = append(suggestions, related...)
		}
	}

	return mergeManualKeywordSuggestions(keyword, suggestions, s.values.Manual.SuggestionsKeep), nil
}

func (s *seoBlogGeneratorSiteIntelligence) requestKeywordSuggestions(ctx context.Context, keyword string, locationCode int) ([]sieDto.ManualKeywordSuggestion, error) {
	req := dto.KeywordSuggestionsRequest{
		Tasks: []dto.KeywordSuggestionsTask{{
			Keyword:            keyword,
			LanguageName:       "English",
			LocationCode:       locationCode,
			Limit:              s.values.Manual.SuggestionsAPILimit,
			ExactMatch:         false,
			IncludeSeedKeyword: false,
			Filters:            [][]interface{}{dto.NewFilter(dto.FilterSearchVolume, dto.FilterOpGreaterThan, 0)},
			OrderBy:            []string{dto.NewOrderBy(dto.OrderByIdeasSearchVolume, dto.OrderDesc)},
		}},
	}

	resp, err := s.dataForSEO.GetKeywordSuggestions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch keyword suggestions for %q: %w", keyword, err)
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return nil, nil
	}

	items := resp.Tasks[0].Result[0].Items
	out := make([]sieDto.ManualKeywordSuggestion, 0, len(items))
	for _, item := range items {
		out = append(out, keywordDataToSuggestion(item, manualSuggestionSourceKeywordSuggestions))
	}
	return out, nil
}

func (s *seoBlogGeneratorSiteIntelligence) requestRelatedKeywords(ctx context.Context, keyword string, locationCode int) ([]sieDto.ManualKeywordSuggestion, error) {
	req := dto.RelatedKeywordsRequest{
		Tasks: []dto.RelatedKeywordsTask{{
			Keyword:      keyword,
			LanguageName: "English",
			LocationCode: locationCode,
			Depth:        s.values.Manual.RelatedKeywordsDepth,
			Limit:        s.values.Manual.SuggestionsAPILimit,
		}},
	}

	resp, err := s.dataForSEO.GetRelatedKeywords(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch related keywords for %q: %w", keyword, err)
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return nil, nil
	}

	items := resp.Tasks[0].Result[0].Items
	out := make([]sieDto.ManualKeywordSuggestion, 0, len(items))
	for _, item := range items {
		out = append(out, keywordDataToSuggestion(item.KeywordData, manualSuggestionSourceRelatedKeywords))
	}
	return out, nil
}

// keywordDataToSuggestion maps a DataForSEO KeywordData row into the wire-level
// suggestion shape, tagging it with the endpoint that produced it.
func keywordDataToSuggestion(item dto.KeywordData, source string) sieDto.ManualKeywordSuggestion {
	suggestion := sieDto.ManualKeywordSuggestion{
		Keyword: item.Keyword,
		Volume:  item.KeywordInfo.SearchVolume,
		CPC:     item.KeywordInfo.CPC,
		Source:  source,
	}
	if item.KeywordProperties != nil && item.KeywordProperties.KeywordDifficulty != nil {
		suggestion.KeywordDifficulty = *item.KeywordProperties.KeywordDifficulty
	}
	if item.SearchIntentInfo != nil {
		suggestion.Intent = item.SearchIntentInfo.MainIntent
	}
	return suggestion
}

// mergeManualKeywordSuggestions applies the manual-add filtering rules to a raw
// suggestion set: it drops empties, zero-volume rows and the exact typed
// keyword, dedupes case-insensitively (preferring the keyword_suggestions
// source over related_keywords on collision), sorts by volume desc then CPC
// desc, and caps the result at keep entries.
func mergeManualKeywordSuggestions(seed string, in []sieDto.ManualKeywordSuggestion, keep int) []sieDto.ManualKeywordSuggestion {
	seedLower := strings.ToLower(strings.TrimSpace(seed))

	byKey := make(map[string]sieDto.ManualKeywordSuggestion, len(in))
	order := make([]string, 0, len(in))

	for _, suggestion := range in {
		kw := strings.TrimSpace(suggestion.Keyword)
		if kw == "" || suggestion.Volume <= 0 {
			continue
		}
		lower := strings.ToLower(kw)
		if lower == seedLower {
			continue
		}
		suggestion.Keyword = kw

		existing, ok := byKey[lower]
		if !ok {
			byKey[lower] = suggestion
			order = append(order, lower)
			continue
		}
		// On a duplicate, prefer the keyword_suggestions source.
		if existing.Source != manualSuggestionSourceKeywordSuggestions && suggestion.Source == manualSuggestionSourceKeywordSuggestions {
			byKey[lower] = suggestion
		}
	}

	merged := make([]sieDto.ManualKeywordSuggestion, 0, len(order))
	for _, k := range order {
		merged = append(merged, byKey[k])
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Volume != merged[j].Volume {
			return merged[i].Volume > merged[j].Volume
		}
		return merged[i].CPC > merged[j].CPC
	})

	if keep > 0 && len(merged) > keep {
		merged = merged[:keep]
	}
	return merged
}

// HandleManualKeywordEnrichment is the async SQS stage. It runs funnel
// classification, opportunity scoring, and cluster assignment sequentially.
// Every persistence op is idempotent so SQS at-least-once redelivery is safe.
func (s *seoBlogGeneratorSiteIntelligence) HandleManualKeywordEnrichment(ctx context.Context, userId string, payload sie.ManualKeywordPayload) error {
	found, kw, err := models.GetKeyword(ctx, payload.KeywordID.Hex())
	if err != nil {
		return fmt.Errorf("failed to hydrate manual keyword: %w", err)
	}
	if !found {
		return fmt.Errorf("manual keyword not found: %s", payload.KeywordID.Hex())
	}
	// Already fully enriched on a prior delivery — nothing to do.
	if kw.Completed {
		return nil
	}

	ok, wec, err := models.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", payload.WebEntityContextID)
	}

	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, wec.WebEntityID.Hex())
	if err != nil {
		return fmt.Errorf("failed to find web entity: %w", err)
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", wec.WebEntityID.Hex())
	}
	businessName := commonutils.Deref(webEntity.BusinessContext.BusinessName)
	productType := commonutils.Deref(webEntity.BusinessContext.ProductType)

	funnel, err := s.classifyFunnelForKeyword(ctx, businessName, productType, *kw)
	if err != nil {
		return fmt.Errorf("failed to classify funnel: %w", err)
	}
	if funnel != "" {
		if err := models.UpdateKeywordFunnels(ctx, map[primitive.ObjectID]models.FunnelStage{kw.ID: funnel}); err != nil {
			return fmt.Errorf("failed to update keyword funnel: %w", err)
		}
		kw.Funnel = funnel
	}

	scoring, userDR := resolveScoringInputs(s.values, webEntity)
	score := computeOpportunityScore(scoring, kw.Volume, kw.KeywordDifficulty, kw.Funnel, userDR)
	if err := models.UpdateKeywordOpportunityScores(ctx, map[primitive.ObjectID]float64{kw.ID: score}); err != nil {
		return fmt.Errorf("failed to update opportunity score: %w", err)
	}
	kw.OpportunityScore = score

	if err := s.assignManualKeywordToCluster(ctx, businessName, productType, kw, wec); err != nil {
		return err
	}

	// Must be the last write.
	if err := models.MarkKeywordsCompleted(ctx, []primitive.ObjectID{kw.ID}); err != nil {
		return fmt.Errorf("failed to mark keyword completed: %w", err)
	}
	return nil
}

func (s *seoBlogGeneratorSiteIntelligence) assignManualKeywordToCluster(ctx context.Context, businessName, productType string, kw *models.Keyword, wec *models.WebEntityContext) error {
	// Idempotency: if a prior delivery already created a cluster pillared on
	// this keyword, re-use it instead of creating a duplicate.
	for _, c := range wec.Clusters {
		if c.PillarKeywordID == kw.ID {
			return models.UpdateKeywordClusters(ctx, map[primitive.ObjectID]string{kw.ID: c.ClusterID})
		}
	}

	keywordTextByID := make(map[primitive.ObjectID]string)
	allKeywords, err := models.GetKeywordsForWEC(ctx, wec.ID.Hex())
	if err != nil {
		return fmt.Errorf("failed to load keywords for cluster context: %w", err)
	}
	for _, k := range allKeywords {
		keywordTextByID[k.ID] = k.Keyword
	}

	decision, err := s.decideClusterForKeyword(ctx, businessName, productType, *kw, wec.Clusters, keywordTextByID)
	if err != nil {
		return fmt.Errorf("failed to assign cluster: %w", err)
	}

	// Normalise the LLM's cluster_id against existing IDs (case-insensitive) so a
	// hallucinated is_new_cluster on an existing slug collapses to assign_existing.
	existingByLowerID := make(map[string]string, len(wec.Clusters))
	for _, c := range wec.Clusters {
		existingByLowerID[strings.ToLower(c.ClusterID)] = c.ClusterID
	}

	decidedID := strings.TrimSpace(decision.ClusterID)
	if actualID, ok := existingByLowerID[strings.ToLower(decidedID)]; ok && decidedID != "" {
		if err := models.UpdateKeywordClusters(ctx, map[primitive.ObjectID]string{kw.ID: actualID}); err != nil {
			return fmt.Errorf("failed to set keyword cluster: %w", err)
		}
		if err := models.AppendKeywordIDToCluster(ctx, wec.ID.Hex(), actualID, kw.ID); err != nil {
			return fmt.Errorf("failed to append keyword to cluster: %w", err)
		}
		return nil
	}

	// Create a new cluster pillared on this keyword. Fall back to a slug derived
	// from the keyword if the LLM omitted a usable id/name.
	newID := decidedID
	if newID == "" {
		newID = slugify(kw.Keyword)
	}
	newName := strings.TrimSpace(decision.ClusterName)
	if newName == "" {
		newName = kw.Keyword
	}

	cluster := models.Cluster{
		ClusterID:            newID,
		ClusterName:          newName,
		PillarKeywordID:      kw.ID,
		PillarIntent:         kw.Intent,
		PillarFunnel:         kw.Funnel,
		SupportingKeywordIDs: []primitive.ObjectID{},
	}
	if err := models.AppendCluster(ctx, wec.ID.Hex(), cluster); err != nil {
		return fmt.Errorf("failed to append new cluster: %w", err)
	}
	if err := models.UpdateKeywordClusters(ctx, map[primitive.ObjectID]string{kw.ID: newID}); err != nil {
		return fmt.Errorf("failed to set keyword cluster: %w", err)
	}
	return nil
}

// classifyFunnelForKeyword runs the shared funnel-classification prompt with a
// 1-element batch and returns the parsed stage ("" when unclassifiable).
func (s *seoBlogGeneratorSiteIntelligence) classifyFunnelForKeyword(ctx context.Context, businessName, productType string, kw models.Keyword) (models.FunnelStage, error) {
	promptDto := sieDto.FunnelClassificationPromptRequest{
		BusinessName:  businessName,
		ProductType:   productType,
		KeywordsBatch: []models.Keyword{kw},
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.FunnelClassification, promptDto)
	if err != nil {
		return "", fmt.Errorf("construct funnel prompt: %w", err)
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("prompt LLM for funnel: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return "", fmt.Errorf("clean funnel response: %w", err)
	}

	var results []funnelClassificationResult
	if err := json.Unmarshal([]byte(cleaned), &results); err != nil {
		return "", fmt.Errorf("unmarshal funnel response: %w", err)
	}
	if len(results) == 0 {
		log.Warn("manual keyword: empty funnel classification response", "keywordId", kw.ID.Hex(), "keyword", kw.Keyword)
		return "", nil
	}

	raw := results[0].Funnel
	for _, r := range results {
		if r.SequenceID == kw.SequenceID {
			raw = r.Funnel
			break
		}
	}
	stage := models.ParseFunnelStage(raw)
	return stage, nil
}

// decideClusterForKeyword runs the manual-keyword cluster-assignment prompt and
// returns the LLM's raw decision (un-normalised).
func (s *seoBlogGeneratorSiteIntelligence) decideClusterForKeyword(ctx context.Context, businessName, productType string, kw models.Keyword, clusters []models.Cluster, keywordTextByID map[primitive.ObjectID]string) (sieDto.ManualKeywordClusterResponse, error) {
	var empty sieDto.ManualKeywordClusterResponse

	summaries := buildClusterSummaries(clusters, keywordTextByID, s.values.ManualClusterExamplesPerCluster)

	promptDto := sieDto.ManualKeywordClusterPromptRequest{
		BusinessName:      businessName,
		ProductType:       productType,
		Keyword:           kw.Keyword,
		Volume:            kw.Volume,
		KeywordDifficulty: kw.KeywordDifficulty,
		Intent:            kw.Intent,
		Funnel:            string(kw.Funnel),
		Clusters:          summaries,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.ManualKeywordCluster, promptDto)
	if err != nil {
		return empty, fmt.Errorf("construct cluster prompt: %w", err)
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.values.ManualClusterMaxTokens,
	})
	if err != nil {
		return empty, fmt.Errorf("prompt LLM for cluster: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return empty, fmt.Errorf("clean cluster response: %w", err)
	}

	var decision sieDto.ManualKeywordClusterResponse
	if err := json.Unmarshal([]byte(cleaned), &decision); err != nil {
		return empty, fmt.Errorf("unmarshal cluster response: %w", err)
	}
	return decision, nil
}

// slugify produces a short hyphenated slug from arbitrary keyword text. Used as
// a fallback cluster_id when the LLM omits one for a new cluster.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
