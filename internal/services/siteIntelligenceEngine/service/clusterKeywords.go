package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/prompts"
	"github.com/atharva-ng/crunch/internal/util/jsonx"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// clusterCountRule renders the prompt's cluster-count instruction: an exact
// count for trial-sized runs, the 6–10 range otherwise (clusterCount == 0).
func clusterCountRule(clusterCount int) string {
	if clusterCount > 0 {
		return fmt.Sprintf("Create exactly %d clusters. No more, no fewer.", clusterCount)
	}
	return "Create between 6 and 10 clusters. No fewer than 6, no more than 10."
}

func (s *seoBlogGeneratorSiteIntelligence) ClusterKeywords(ctx context.Context, userId string, metadata siteIntelligenceEngine.SIEMetadata) error {
	ok, webEntityContext, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", metadata.WebEntityContextID)
	}

	// Entry guard (P2) — the explicit requirement: if clustering already completed,
	// this message must NOT be processed. Return before any LLM call, cluster
	// overwrite, or Scheduling re-dispatch. EffectiveStatus resolves the error
	// sentinel to real progress so an errored-after-clustering WEC also skips.
	if webEntityContext.EffectiveStatus() >= models.SIEStatusClusteringDone {
		return nil
	}

	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, webEntityContext.WebEntityID.Hex())
	if err != nil {
		return fmt.Errorf("failed to find web entity: %w", err)
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", webEntityContext.WebEntityID.Hex())
	}

	// Claim (P3): move into ClusteringStarted. ClusteringStarted is in the from-set
	// so a crashed prior attempt can re-run (P6); SIEStatusError so a retry whose
	// prior attempt errored can re-claim. A redelivery that loses the CAS exits
	// without re-running the LLM.
	claimed, err := models.TryAdvanceStatus(ctx, metadata.WebEntityContextID,
		[]int{models.SIEStatusOpportunityScoreCalculated, models.SIEStatusClusteringStarted, models.SIEStatusError},
		models.SIEStatusClusteringStarted)
	if err != nil {
		return fmt.Errorf("failed to claim clustering: %w", err)
	}
	if !claimed {
		return nil
	}

	processed, err := models.GetKeywordsForWEC(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to load processed keywords: %w", err)
	}
	if len(processed) == 0 {
		return fmt.Errorf("no processed keywords found for web entity context %s", metadata.WebEntityContextID)
	}

	// Only cluster keywords whose funnel classification has completed. Keywords
	// that failed (or were skipped by) funnel classification carry an empty
	// Funnel, and clustering reasons over funnel stage to pick pillars — so feed
	// the model only fully-classified keywords.
	classified := make([]models.Keyword, 0, len(processed))
	for _, kw := range processed {
		if kw.Funnel != "" {
			classified = append(classified, kw)
		}
	}
	if len(classified) == 0 {
		return fmt.Errorf("no funnel-classified keywords found for web entity context %s", metadata.WebEntityContextID)
	}

	clusteringKeywords := make([]sieDto.ClusturingKeyword, len(classified))
	for i, kw := range classified {
		clusteringKeywords[i] = sieDto.ClusturingKeyword{
			SequenceID: kw.SequenceID,
			Keyword:    kw.Keyword,
		}
	}

	// keywordTextByID/keywordByID resolve keyword IDs (including the pillar /
	// supporting IDs stored on any pre-existing clusters) back to text and full
	// docs. Built from the full processed set so a re-cluster can describe
	// clusters whose members aren't all in the classified subset.
	keywordTextByID := make(map[primitive.ObjectID]string, len(processed))
	keywordByID := make(map[primitive.ObjectID]models.Keyword, len(processed))
	for _, kw := range processed {
		keywordTextByID[kw.ID] = kw.Keyword
		keywordByID[kw.ID] = kw
	}

	// If this WEC was clustered before (upgrade re-run over trial clusters, or
	// a crashed attempt's partial write), hand the existing cluster map to the
	// model so it reuses the same cluster_id / cluster_name where the topic
	// still holds, instead of regenerating an unrelated map on every re-run —
	// and switch the prompt into merge semantics: grow the map, keep every
	// existing cluster_id, and don't move keywords that already hold calendar
	// slots (their cluster/day spread must stay honest).
	existingClusters := buildClusterSummaries(webEntityContext.Clusters, keywordTextByID, s.values.ManualClusterExamplesPerCluster)

	scheduled, err := models.GetScheduledArticlesByWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to load scheduled articles for cluster freeze list: %w", err)
	}
	scheduledTexts := make([]string, 0, len(scheduled))
	for _, sa := range scheduled {
		if text, found := keywordTextByID[sa.KeywordID]; found {
			scheduledTexts = append(scheduledTexts, text)
		}
	}

	bc := webEntity.BusinessContext
	if bc == nil {
		return fmt.Errorf("web entity %s has no business context", webEntity.ID.Hex())
	}
	promptDto := sieDto.ClusturingPromptRequest{
		BusinessName:      commonutils.Deref(bc.BusinessName),
		ProductType:       commonutils.Deref(bc.ProductType),
		Integrations:      strings.Join(bc.Integrations, ", "),
		KeywordsBatch:     clusteringKeywords,
		ExistingClusters:  existingClusters,
		ClusterCountRule:  clusterCountRule(s.limitsFor(webEntityContext).ClusterCount),
		UpgradeMerge:      len(existingClusters) > 0,
		ScheduledKeywords: strings.Join(scheduledTexts, ", "),
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.KeywordClustering, promptDto)
	if err != nil {
		return fmt.Errorf("failed to construct clustering prompt: %w", err)
	}

	// Clustering must echo a cluster assignment for every keyword (up to
	// expandedKeywordsLimit), so its output dwarfs the default token budget.
	// Use the dedicated clustering budget to avoid truncated, unparseable JSON.
	maxTokens := s.values.ClusterMaxTokens
	if maxTokens <= 0 {
		maxTokens = s.LLM.DefaultMaxTokens
	}

	promptReq := dto.PromptRequest{
		Messages: []dto.Message{
			{Role: "user", Content: prompt},
		},
		Model:     dto.AnthropicOpus48,
		MaxTokens: maxTokens,
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, promptReq)
	if err != nil {
		return fmt.Errorf("failed to prompt LLM for clustering: %w", err)
	}

	// A max_tokens stop means the JSON is truncated; surface that explicitly
	// instead of letting it fail downstream as an opaque "unexpected end of
	// JSON input" parse error.
	if response.StopReason == "max_tokens" {
		return fmt.Errorf("clustering response truncated at max_tokens (%d); raise siteIntelligence.clusterMaxTokens", maxTokens)
	}

	cleanedResponse, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return fmt.Errorf("failed to clean LLM response: %w", err)
	}

	jsonBody, err := jsonx.ExtractObject(cleanedResponse)
	if err != nil {
		return fmt.Errorf("failed to locate clustering JSON: %w", err)
	}

	var result sieDto.ClusteringResponse
	if err := json.Unmarshal(jsonBody, &result); err != nil {
		return fmt.Errorf("failed to unmarshal clustering response: %w", err)
	}

	// Resolve the model's pillar/supporting references against the classified
	// set it was actually given, so a hallucinated sequence_id outside that set
	// is dropped rather than silently matched.
	keywordBySeqID := make(map[int]models.Keyword, len(classified))
	keywordByName := make(map[string]models.Keyword, len(classified))
	for _, kw := range classified {
		keywordBySeqID[kw.SequenceID] = kw
		keywordByName[strings.ToLower(kw.Keyword)] = kw
	}

	keywordClusters := make(map[primitive.ObjectID]string)
	clusters := make([]models.Cluster, 0, len(result.Clusters))
	resultIDs := make(map[string]bool, len(result.Clusters))

	for _, llmC := range result.Clusters {
		resultIDs[llmC.ClusterID] = true
		pillarKeyword, pillarExists := keywordByName[strings.ToLower(llmC.PillarKeyword)]
		if pillarExists {
			keywordClusters[pillarKeyword.ID] = llmC.ClusterID
		}

		supportingIDs := make([]primitive.ObjectID, 0, len(llmC.SupportingKeywords))
		type supportingEntry struct {
			id    primitive.ObjectID
			score float64
		}
		supportingEntries := make([]supportingEntry, 0, len(llmC.SupportingKeywords))
		for _, seqID := range llmC.SupportingKeywords {
			kw, exists := keywordBySeqID[seqID]
			if !exists {
				continue
			}
			if pillarExists && kw.ID == pillarKeyword.ID {
				continue
			}
			supportingEntries = append(supportingEntries, supportingEntry{id: kw.ID, score: kw.OpportunityScore})
			keywordClusters[kw.ID] = llmC.ClusterID
		}
		sort.Slice(supportingEntries, func(a, b int) bool {
			return supportingEntries[a].score > supportingEntries[b].score
		})
		for _, e := range supportingEntries {
			supportingIDs = append(supportingIDs, e.id)
		}

		pillarFunnel := models.ParseFunnelStage(llmC.PillarFunnel)

		clusters = append(clusters, models.Cluster{
			ClusterID:            llmC.ClusterID,
			ClusterName:          llmC.ClusterName,
			PillarKeywordID:      pillarKeyword.ID,
			PillarIntent:         llmC.PillarIntent,
			PillarFunnel:         pillarFunnel,
			SupportingKeywordIDs: supportingIDs,
		})
	}

	// Growth invariant: an existing cluster_id the model dropped anyway is
	// preserved verbatim (its published pillar / calendar references must keep
	// resolving), and its members are pinned to their stored cluster value so
	// the changed-only write below can't move them.
	for _, existing := range webEntityContext.Clusters {
		if resultIDs[existing.ClusterID] {
			continue
		}
		clusters = append(clusters, existing)
		for _, id := range append([]primitive.ObjectID{existing.PillarKeywordID}, existing.SupportingKeywordIDs...) {
			if kw, found := keywordByID[id]; found {
				keywordClusters[id] = kw.Cluster
			}
		}
	}

	// Sort clusters by pillar opportunity score descending so the persisted
	// order matches the read endpoint's expected ordering.
	sort.Slice(clusters, func(i, j int) bool {
		return keywordByID[clusters[i].PillarKeywordID].OpportunityScore >
			keywordByID[clusters[j].PillarKeywordID].OpportunityScore
	})

	if len(keywordClusters) > 0 {
		// $set keyword.cluster only where it actually changed — on a re-run the
		// bulk of the set is already assigned and pinned.
		changed := make(map[primitive.ObjectID]string, len(keywordClusters))
		for id, clusterID := range keywordClusters {
			if kw, found := keywordByID[id]; found && kw.Cluster != clusterID {
				changed[id] = clusterID
			}
		}
		if len(changed) > 0 {
			if err := models.UpdateKeywordClusters(ctx, changed); err != nil {
				return fmt.Errorf("failed to update keyword clusters: %w", err)
			}
		}
		// Clustering is the terminal enrichment stage — flag every clustered
		// keyword as completed so the read path can distinguish fully-enriched
		// keywords from in-flight manually-added ones.
		completedIDs := make([]primitive.ObjectID, 0, len(keywordClusters))
		for id := range keywordClusters {
			completedIDs = append(completedIDs, id)
		}
		if err := models.MarkKeywordsCompleted(ctx, completedIDs); err != nil {
			return fmt.Errorf("failed to mark keywords completed: %w", err)
		}
	}

	// Persist the cluster set ($set, converges on re-run). The status advance is a
	// separate CAS so completion is exactly-once even if two workers raced through
	// the ClusteringStarted claim above.
	if err := models.UpdateWebEntityContext(ctx, metadata.WebEntityContextID, models.WECUpdateReq{
		Clusters: clusters,
	}); err != nil {
		return fmt.Errorf("failed to save clusters: %w", err)
	}

	// Advance + dispatch-once (P3): only the worker that flips ClusteringStarted →
	// ClusteringDone hands off to the Scheduling Engine, stopping the duplicate
	// Scheduling dispatch. SIEStatusError is in the from-set so a retry that re-ran
	// clustering after an error still completes.
	claimed, err = models.TryAdvanceStatus(ctx, metadata.WebEntityContextID,
		[]int{models.SIEStatusClusteringStarted, models.SIEStatusError},
		models.SIEStatusClusteringDone)
	if err != nil {
		return fmt.Errorf("failed to advance clustering to done: %w", err)
	}
	if !claimed {
		return nil
	}

	return s.pipeline.DispatchNext(ctx, siteIntelligenceEngine.ProcessSIEClustering, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        webEntityContext.WebEntityID.Hex(),
		WebEntityContextID: metadata.WebEntityContextID,
	})
}
