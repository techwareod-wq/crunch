package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (s *seoBlogGeneratorSiteIntelligence) PostProcessing(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	_, webEntityContext, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}

	// Entry guard (P2): post-processing (or a later stage) already done — drop the
	// redelivery without re-running the delete+insert or re-fanning-out funnel
	// chunks (the double fan-out that reset `total` and re-emitted every chunk).
	if webEntityContext.EffectiveStatus() >= models.SIEStatusPostProcessingDone {
		return nil
	}

	// Claim (P3): exactly one worker moves a pre-post-processing status into
	// PostProcessingStarted. This also absorbs the data-fetch fan-in — several
	// data-fetch edges point at PostProcessing, so it can be dispatched more than
	// once; every loser of the CAS returns without doing work. SIEStatusError is
	// in the from-set so a retry whose prior attempt errored can re-claim;
	// PostProcessingStarted is in it so a crashed claim can re-run (P6).
	claimed, err := models.TryAdvanceStatus(ctx, metadata.WebEntityContextID,
		[]int{models.SIEStatusCreated, models.SIEStatusProcessing, models.SIEStatusPostProcessingStarted, models.SIEStatusError},
		models.SIEStatusPostProcessingStarted)
	if err != nil {
		return fmt.Errorf("failed to claim post-processing: %w", err)
	}
	if !claimed {
		return nil
	}

	merged := mergeKeywords(webEntityContext.Keywords)

	// The dedupe passes rank variants by a provisional opportunity score
	// (funnel is unclassified this early, so its term is the default weight),
	// which needs the strategy-tilted scoring knobs and the user's domain
	// rating.
	isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, metadata.WebEntityID)
	if err != nil {
		return fmt.Errorf("failed to find web entity: %w", err)
	}
	if !isWebEntity {
		return fmt.Errorf("web entity not found: %s", metadata.WebEntityID)
	}
	scoring, userDR := resolveScoringInputs(s.values, webEntity)

	// Dedupe-effectiveness counters, reported in one summary log at the end.
	poolDupesRemoved := 0      // variant collapse within the fresh pool
	candidateDupesDropped := 0 // fresh candidates that lost to stored/kept keywords
	storedDeleted := 0         // stored keywords outscored by a fresh variant

	processingSteps := []keywordProcessingSteps{
		FilterNull,
		// filterNavigationalIntent,
		// FilterByKeywordDifficulty(5),
		FilterByMinCPC(0),
		// FilterByRankingPosition(0),
		FilterByMinVolume(5),
		CountRemoved(Deduplicate(scoring, userDR), &poolDupesRemoved),
		// Trial cap (no-op at 0/full): keep only the strongest keywords, so it
		// must run after the filters and before sequence IDs are assigned.
		TruncateTopKeywords(s.limitsFor(webEntityContext).MaxPersistedKeywords),
		FlagRefreshCandidates(s.values.RefreshCandidate.MinPosition, s.values.RefreshCandidate.MaxPosition),
	}

	processed := applyProcessingSteps(merged, processingSteps)

	if len(processed) == 0 {
		return fmt.Errorf("no keywords remaining after filtering and deduplication")
	}

	existing, err := models.GetKeywordsForWEC(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to load existing keywords: %w", err)
	}

	if len(existing) > 0 {
		// Keyword docs already exist (upgrade re-run over trial data, or a
		// retry that crashed after insert): run the merged variant dedupe over
		// existing ∪ fresh. Old keywords compete on provisional opportunity
		// score instead of auto-winning — a stored keyword that loses its
		// variant group is deleted (and pulled from cluster supporting lists),
		// EXCEPT protected ones: any keyword an article row hangs off (the
		// scheduledArticle row doubles as the "article exists" flag — deleting
		// the article deletes the row, which un-protects the keyword) and
		// cluster pillars. Never a wholesale wipe.
		sas, err := models.GetScheduledArticlesByWebEntityContext(ctx, metadata.WebEntityContextID)
		if err != nil {
			return fmt.Errorf("failed to load scheduled articles for dedupe protection: %w", err)
		}
		protected := make(map[primitive.ObjectID]struct{}, len(sas)+len(webEntityContext.Clusters))
		for _, sa := range sas {
			protected[sa.KeywordID] = struct{}{}
		}
		for _, cl := range webEntityContext.Clusters {
			protected[cl.PillarKeywordID] = struct{}{}
		}

		outcome := dedupeMergedKeywords(processed, existing, protected, scoring, userDR)
		candidateDupesDropped = len(processed) - len(outcome.insert)
		storedDeleted = len(outcome.deleteIDs)
		if len(outcome.deleteIDs) > 0 {
			if err := models.DeleteKeywordsByIDs(ctx, outcome.deleteIDs); err != nil {
				return fmt.Errorf("failed to delete outscored keyword variants: %w", err)
			}
			if err := models.RemoveKeywordIDsFromClusters(ctx, metadata.WebEntityContextID, outcome.deleteIDs); err != nil {
				return fmt.Errorf("failed to remove deduped keywords from clusters: %w", err)
			}
			log.Info("post-processing: removed stored keywords outscored by fresh variants",
				"webEntityContextId", metadata.WebEntityContextID, "count", len(outcome.deleteIDs))
		}
		if len(outcome.insert) == 0 {
			log.Warn("post-processing: no new keywords survived dedupe against existing set",
				"webEntityContextId", metadata.WebEntityContextID)
		} else {
			maxSeq, err := models.GetMaxSequenceIDForWEC(ctx, metadata.WebEntityContextID)
			if err != nil {
				return fmt.Errorf("failed to get max sequence ID: %w", err)
			}
			newDocs := outcome.insert
			for i := range newDocs {
				newDocs[i].SequenceID = maxSeq + 1 + i
			}
			if _, err := models.InsertKeywords(ctx, metadata.WebEntityContextID, newDocs); err != nil {
				return fmt.Errorf("failed to persist merged keywords: %w", err)
			}
		}
	} else {
		// Fresh run: insert the processed set wholesale.
		processed = applyProcessingSteps(processed, []keywordProcessingSteps{Sequentialise})
		if _, err := models.InsertKeywords(ctx, metadata.WebEntityContextID, processed); err != nil {
			return fmt.Errorf("failed to persist processed keywords: %w", err)
		}
	}

	log.Info("post-processing: dedupe summary",
		"webEntityContextId", metadata.WebEntityContextID,
		"poolDupesRemoved", poolDupesRemoved,
		"candidateDupesDropped", candidateDupesDropped,
		"storedKeywordsDeleted", storedDeleted,
		"totalDuplicatesRemoved", poolDupesRemoved+candidateDupesDropped+storedDeleted)

	// Advance + dispatch-once (P3): only the worker that flips PostProcessingStarted
	// → PostProcessingDone fans out funnel classification. This is what prevents the
	// double fan-out (which reset `total` and re-emitted all chunks). SIEStatusError
	// is in the from-set so a retry that re-ran the body after an error still hands
	// off.
	claimed, err = models.TryAdvanceStatus(ctx, metadata.WebEntityContextID,
		[]int{models.SIEStatusPostProcessingStarted, models.SIEStatusError},
		models.SIEStatusPostProcessingDone)
	if err != nil {
		return fmt.Errorf("failed to advance post-processing to done: %w", err)
	}
	if !claimed {
		return nil
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEPostProcessing, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        metadata.WebEntityID,
		WebEntityContextID: metadata.WebEntityContextID,
	})
}

func (s *seoBlogGeneratorSiteIntelligence) DispatchFunnelClassification(ctx context.Context, userId, webEntityContextID string) error {
	_, wec, err := models.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}

	processed, err := models.GetKeywordsForWEC(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to load processed keywords: %w", err)
	}
	if len(processed) == 0 {
		return fmt.Errorf("no processed keywords found for web entity context %s", webEntityContextID)
	}

	// Retrigger re-runs funnel classification from scratch on the existing
	// processed keywords. Clear their prior funnel state first, otherwise the
	// state-derived completion gate would see them as already settled and advance
	// before re-classification ran. Then rewind status to the fan-out's CAS
	// predecessor (PostProcessingDone) so the now single-shot fan-out claims it —
	// the fan-out only emits chunks on PostProcessingDone → FunnelClassificationStarted.
	if err := models.ResetKeywordFunnelState(ctx, webEntityContextID); err != nil {
		return fmt.Errorf("failed to reset keyword funnel state for retrigger: %w", err)
	}
	if err := models.UpdateWebEntityContext(ctx, webEntityContextID, models.WECUpdateReq{
		Status: commonutils.Ptr(models.SIEStatusPostProcessingDone),
	}); err != nil {
		return fmt.Errorf("failed to reset status for funnel retrigger: %w", err)
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEPostProcessing, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextID,
	})
}

func (s *seoBlogGeneratorSiteIntelligence) DispatchOpportunityScore(ctx context.Context, userId, webEntityContextID string) error {
	ok, wec, err := models.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}

	// Gate on EffectiveStatus, not Status: an errored WEC reports SIEStatusError,
	// so checking the raw status would let a run that errored *before* funnel
	// classification finished jump straight into opportunity score (and thence
	// clustering). EffectiveStatus resolves to the real progress stage.
	eff := wec.EffectiveStatus()
	if eff != models.SIEStatusFunnelClassificationDone &&
		eff != models.SIEStatusOpportunityScoreCalculated {
		return fmt.Errorf("cannot retrigger opportunity score: invalid effective status %d, expected funnel classification done or opportunity score calculated", eff)
	}

	// Rewind status to OpportunityScore's claim predecessor so the (now CAS-gated)
	// handler can advance. After the stage already ran once the raw status sits at
	// OpportunityScoreCalculated/later, which the advance CAS (from
	// FunnelClassificationDone) would no longer match — the retrigger would
	// silently no-op. Resetting to FunnelClassificationDone restores the off-by-one
	// re-run contract.
	if err := models.UpdateWebEntityContext(ctx, webEntityContextID, models.WECUpdateReq{
		Status: commonutils.Ptr(models.SIEStatusFunnelClassificationDone),
	}); err != nil {
		return fmt.Errorf("failed to reset status for opportunity score retrigger: %w", err)
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEFunnelClassification, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextID,
	})
}

func (s *seoBlogGeneratorSiteIntelligence) DispatchClustering(ctx context.Context, userId, webEntityContextID string) error {
	ok, wec, err := models.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}

	// Gate on EffectiveStatus, not Status (see DispatchOpportunityScore): an
	// errored WEC reports SIEStatusError, so the raw status can't tell whether
	// opportunity scoring actually completed. EffectiveStatus resolves to the
	// real progress stage, keeping clustering from running ahead of its inputs.
	eff := wec.EffectiveStatus()
	if eff != models.SIEStatusOpportunityScoreCalculated &&
		eff != models.SIEStatusClusteringStarted {
		return fmt.Errorf("cannot retrigger clustering: invalid effective status %d, expected opportunity score calculated or clustering started", eff)
	}

	// Clear clusters + error data and rewind status to Clustering's claim
	// predecessor (OpportunityScoreCalculated) in one write. The status reset is
	// required so the CAS-gated clustering handler can claim
	// OpportunityScoreCalculated → ClusteringStarted; without it a fully-clustered
	// WEC (status ClusteringDone) would fail the claim and the retrigger would
	// no-op.
	if err := models.UpdateWebEntityContext(ctx, webEntityContextID, models.WECUpdateReq{
		Clusters:  []models.Cluster{},
		ErrorData: []models.SIEErrorData{},
		Status:    commonutils.Ptr(models.SIEStatusOpportunityScoreCalculated),
	}); err != nil {
		return fmt.Errorf("failed to clear clusters before retrigger: %w", err)
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEOpportunityScore, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextID,
	})
}
