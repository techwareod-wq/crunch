package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func buildPipeline(dispatcher pipeline.Dispatcher, funnelClassificationChunkSize int) *pipeline.Pipeline {
	p, err := pipeline.NewBuilder(dispatcher).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEGetUserKeywords, Label: "Get User Keywords"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEGetCompetitorKeywords, Label: "Get Competitor Keywords"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEGetPreExpandedUserKeywords, Label: "Get Pre-Expanded Keywords"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEGetExpandedUserKeywords, Label: "Get Expanded Keywords"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEPostProcessing, Label: "Post Processing"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEFunnelClassification, Label: "Funnel Classification"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEOpportunityScore, Label: "Opportunity Score"}).
		AddStage(pipeline.Stage{ProcessType: sie.ProcessSIEClustering, Label: "Clustering"}).
		// SIE owns the trigger into the Scheduling Engine. Adding the stage
		// + edge here keeps the cross-engine handoff visible in the SIE DAG
		// and lets future maintainers trace it via Successors().
		AddStage(pipeline.Stage{ProcessType: se.ProcessSchedulingOrchestrate, Label: "Scheduling Orchestrate"}).
		AddEdge(sie.ProcessSIEGetUserKeywords, sie.ProcessSIEPostProcessing, nil).
		AddEdge(sie.ProcessSIEGetCompetitorKeywords, sie.ProcessSIEPostProcessing, nil).
		AddEdge(sie.ProcessSIEGetExpandedUserKeywords, sie.ProcessSIEPostProcessing, nil).
		AddEdge(sie.ProcessSIEGetPreExpandedUserKeywords, sie.ProcessSIEGetExpandedUserKeywords, nil).
		AddEdge(sie.ProcessSIEPostProcessing, sie.ProcessSIEFunnelClassification, dispatchFunnelClassificationFanOut(funnelClassificationChunkSize)).
		AddEdge(sie.ProcessSIEFunnelClassification, sie.ProcessSIEOpportunityScore, nil).
		AddEdge(sie.ProcessSIEOpportunityScore, sie.ProcessSIEClustering, nil).
		AddEdge(sie.ProcessSIEClustering, se.ProcessSchedulingOrchestrate, dispatchSchedulingOrchestrate).
		Build()

	if err != nil {
		panic(fmt.Errorf("failed to build SIE pipeline: %w", err))
	}
	return p
}

// dispatchSchedulingOrchestrate hands off control to the Scheduling Engine
// after clustering completes. Sends a typed SEOrchestratePayload rather than
// the default StandardPayload so the SE handler doesn't need to know about
// SIE's internal payload shape.
//
// An upgrade re-run hands off to SE_EXTEND_SCHEDULE instead: the trial
// calendar already exists, so SE Orchestrate's count>0 guard would no-op and
// the full calendar would never be appended. ExtendSchedule appends the
// remaining slots and finalizes the upgrade.
func dispatchSchedulingOrchestrate(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	payload := se.SEOrchestratePayload{
		WebEntityID:        dc.WebEntityID,
		WebEntityContextID: dc.WebEntityContextID,
	}

	ok, wec, err := models.GetWebEntityContext(ctx, dc.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("scheduling hand-off: get web entity context: %w", err)
	}
	if ok && wec.UpgradeState == models.WECUpgradeStateExpanding {
		return d.Dispatch(ctx, string(se.ProcessSchedulingExtendSchedule), dc.UserID, payload)
	}
	return d.Dispatch(ctx, string(next), dc.UserID, payload)
}

// dispatchFunnelClassificationFanOut returns the edge callback that fans the
// classified keywords out into chunks. chunkSize (values.siteIntelligence.
// funnelClassificationChunkSize) is captured in the closure since the
// DispatchFunc signature carries no place to thread it.
func dispatchFunnelClassificationFanOut(chunkSize int) pipeline.DispatchFunc {
	return func(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
		// Single-shot fan-out (P3): only the worker that wins the
		// PostProcessingDone → FunnelClassificationStarted transition emits chunks.
		// This edge fires both from the winning PostProcessing handler and from an
		// Orchestrate resume (DispatchNext(PostProcessing)); without the CAS a
		// redelivered/resumed edge would reset `total` to a stale value and re-emit
		// the whole ~N-chunk set, the core of the queue amplification (RC6).
		claimed, err := models.TryAdvanceStatus(ctx, dc.WebEntityContextID,
			[]int{models.SIEStatusPostProcessingDone},
			models.SIEStatusFunnelClassificationStarted)
		if err != nil {
			return fmt.Errorf("fan-out: claim funnel start: %w", err)
		}
		if !claimed {
			return nil
		}

		keywords, err := models.GetKeywordsForWEC(ctx, dc.WebEntityContextID)
		if err != nil {
			return fmt.Errorf("fan-out: load keywords: %w", err)
		}
		if len(keywords) == 0 {
			return fmt.Errorf("fan-out: no processed keywords for %s", dc.WebEntityContextID)
		}

		ids := make([]primitive.ObjectID, len(keywords))
		for i, kw := range keywords {
			ids[i] = kw.ID
		}

		// Record total for display/diagnostics and clear the legacy failed-id list.
		// Status was already advanced by the CAS above, so it is intentionally not
		// set here. The completion gate no longer reads these counters (it derives
		// progress from keyword state), so this is bookkeeping only.
		if err := models.UpdateWebEntityContext(ctx, dc.WebEntityContextID, models.WECUpdateReq{
			ProcessMetadata: &models.ProcessMetadata{
				FunnelClassificationMetadata: models.FunnelClassificationMetadata{
					FunnelClassificationTotal:            len(ids),
					FunnelClassificationProcessed:        0,
					FunnelClassificationFailed:           0,
					FunnelClassificationFailedKeywordIDs: []primitive.ObjectID{},
				},
			},
		}); err != nil {
			return fmt.Errorf("fan-out: init metadata: %w", err)
		}

		for i := 0; i < len(ids); i += chunkSize {
			end := i + chunkSize
			if end > len(ids) {
				end = len(ids)
			}

			payload := sie.FunnelClassificationPayload{
				WebEntityContextID: dc.WebEntityContextID,
				KeywordIDs:         ids[i:end],
				IsRetry:            false,
			}

			if err := d.Dispatch(ctx, string(next), dc.UserID, payload); err != nil {
				return fmt.Errorf("fan-out: dispatch chunk [%d:%d]: %w", i, end, err)
			}
		}

		return nil
	}
}
