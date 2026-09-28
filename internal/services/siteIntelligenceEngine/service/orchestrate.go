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
	"go.mongodb.org/mongo-driver/mongo"
)

func (s *seoBlogGeneratorSiteIntelligence) Orchestrate(ctx context.Context, userId, webEntityId string) error {
	isWebEntityContext, webEntityContext, err := models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityId, userId)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}

	webEntityContextId := ""
	if isWebEntityContext {
		webEntityContextId = webEntityContext.ID.Hex()

		// Upgrade re-run: the kickoff rewound the cursor (sie_mode already full)
		// and the WEC is mid-re-run — fall through to dispatch the incomplete
		// stages instead of treating the healthy status as "in flight, no-op".
		// Every stage claim is CAS-guarded, so a duplicate entry here only
		// re-dispatches messages the losers of those claims will drop.
		upgradeRerun := webEntityContext.UpgradeState == models.WECUpgradeStateExpanding &&
			webEntityContext.EffectiveSIEMode() == models.SIEModeFull

		if webEntityContext.Status != models.SIEStatusError {
			if !upgradeRerun {
				return nil
			}
		} else {
			// Errored run: clear error data and resume from the last completed stage.
			resetStatus := computeResetStatus(webEntityContext)
			webEntityContext.Status = resetStatus

			if err := models.UpdateWebEntityContext(ctx, webEntityContextId, models.WECUpdateReq{
				ErrorData: []models.SIEErrorData{},
				Status:    commonutils.Ptr(resetStatus),
			}); err != nil {
				return fmt.Errorf("failed to reset for retry: %w", err)
			}
		}
	} else {
		userOID, err := primitive.ObjectIDFromHex(userId)
		if err != nil {
			return fmt.Errorf("invalid user ID: %w", err)
		}
		webEntityOID, err := primitive.ObjectIDFromHex(webEntityId)
		if err != nil {
			return fmt.Errorf("invalid web entity ID: %w", err)
		}
		webEntityContext = &models.WebEntityContext{
			UserID:      userOID,
			WebEntityID: webEntityOID,
			Status:      models.SIEStatusCreated,
			SIEMode:     resolveSIEMode(ctx, userId),
			ProcessMetadata: models.ProcessMetadata{
				DataFetchStepStatus: &models.SIEDataFetchStepStatus{},
			},
		}
		if err := models.CreateWebEntityContext(ctx, webEntityContext); err != nil {
			if !mongo.IsDuplicateKeyError(err) {
				return fmt.Errorf("failed to create web entity context: %w", err)
			}

			// Race lost: a concurrent step read created the WEC first (the
			// {user_id, web_entity_id} unique index rejected this insert). Re-read
			// and treat it as the existing-WEC path — the winner's WEC is at
			// Created/Processing, so the idempotency guard below no-ops; an
			// errored one resumes.
			found, existing, rerr := models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityId, userId)
			if rerr != nil {
				return fmt.Errorf("re-read after dup key: %w", rerr)
			}
			if !found {
				return fmt.Errorf("web entity context missing after dup key for web entity %s", webEntityId)
			}
			webEntityContext = existing
			webEntityContextId = existing.ID.Hex()

			if existing.Status != models.SIEStatusError {
				return nil // in-flight/done — no-op
			}

			// Errored run: clear error data and resume from the last completed stage.
			resetStatus := computeResetStatus(existing)
			webEntityContext.Status = resetStatus
			if err := models.UpdateWebEntityContext(ctx, webEntityContextId, models.WECUpdateReq{
				ErrorData: []models.SIEErrorData{},
				Status:    commonutils.Ptr(resetStatus),
			}); err != nil {
				return fmt.Errorf("failed to reset for retry: %w", err)
			}
		} else {
			webEntityContextId = webEntityContext.ID.Hex()
		}
	}

	dataFetchStatus := webEntityContext.ProcessMetadata.DataFetchStepStatus
	if dataFetchStatus == nil {
		dataFetchStatus = &models.SIEDataFetchStepStatus{}
	}

	// Dispatch the incomplete steps. A dispatch failure (SQS error, etc.) before
	// any worker runs would otherwise strand the WEC at Created/the reset status
	// with no worker to advance it, and the retrigger-on-error rule wouldn't touch
	// it (status != Error). So on failure we flip the WEC to Error, letting the
	// next payment-gated /v1/onboarding-steps poll resume from the last completed
	// stage.
	var dispatchErr error
	if dataFetchStatus.AllDone() {
		dispatchErr = s.dispatchIncompletePostDataFetchSteps(ctx, userId, webEntityContext, webEntityContextId)
	} else {
		dispatchErr = s.dispatchIncompleteDataFetchSteps(ctx, userId, webEntityId, webEntityContextId, dataFetchStatus)
	}
	if dispatchErr != nil {
		// Best-effort diagnostic: recording the dispatch failure aids observability
		// but is not part of the control flow — the real error is returned below
		// regardless — so a failed AppendErrorData is intentionally discarded and
		// only surfaced at debug level.
		if appendErr := models.AppendErrorData(ctx, webEntityContextId, models.SIEErrorData{
			Step:    "orchestrate_dispatch",
			Message: dispatchErr.Error(),
		}); appendErr != nil {
			log.Debug("SIE orchestrate: failed to append dispatch error diagnostic (best-effort)",
				"error", appendErr, "webEntityContextId", webEntityContextId)
		}
		return dispatchErr
	}
	return nil
}

// computeResetStatus walks the pipeline right-to-left and returns the status
// of the last completed stage so the orchestrator resumes from the right point.
// It reads EffectiveStatus, not Status: an errored WEC has Status ==
// SIEStatusError, and the real progress lives in ProcessMetadata.LastStatus.
func computeResetStatus(wec *models.WebEntityContext) int {
	progress := wec.EffectiveStatus()
	if progress >= models.SIEStatusOpportunityScoreCalculated {
		return models.SIEStatusOpportunityScoreCalculated
	}
	if progress >= models.SIEStatusFunnelClassificationDone {
		return models.SIEStatusFunnelClassificationDone
	}
	if progress >= models.SIEStatusPostProcessingDone {
		return models.SIEStatusPostProcessingDone
	}
	return models.SIEStatusProcessing
}

func (s *seoBlogGeneratorSiteIntelligence) dispatchIncompleteDataFetchSteps(
	ctx context.Context, userId, webEntityId, webEntityContextId string,
	status *models.SIEDataFetchStepStatus,
) error {
	payload := pipeline.StandardPayload{
		WebEntityID:        webEntityId,
		WebEntityContextID: webEntityContextId,
	}

	if !status.UserKeywordsDone {
		if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEGetUserKeywords), userId, payload); err != nil {
			return fmt.Errorf("dispatch user keywords: %w", err)
		}
	}

	if !status.CompetitorKeywordsDone {
		isWebEntity, webEntity, err := models.FindWebEntityByID(ctx, webEntityId)
		if err != nil {
			return fmt.Errorf("failed to find web entity: %w", err)
		}
		if !isWebEntity {
			return fmt.Errorf("web entity not found: %s", webEntityId)
		}

		if err := models.UpdateWebEntityContext(ctx, webEntityContextId, models.WECUpdateReq{
			Keywords:                    &models.KeyWords{CompetitorKeyWords: []models.Keyword{}},
			CompetitorKeywordsProcessed: commonutils.Ptr(0),
			TotalCompetitors:            commonutils.Ptr(len(webEntity.Competitors)),
		}); err != nil {
			return fmt.Errorf("failed to reset competitor keywords progress: %w", err)
		}

		for _, competitor := range webEntity.Competitors {
			payload := pipeline.StandardPayload{
				WebEntityID:        webEntityId,
				WebEntityContextID: webEntityContextId,
				CompetitorURL:      competitor.Domain,
			}
			if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEGetCompetitorKeywords), userId, payload); err != nil {
				return fmt.Errorf("dispatch competitor keywords for %s: %w", competitor.Domain, err)
			}
		}
	}

	if !status.PreExpandedKeywordsDone {
		if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEGetPreExpandedUserKeywords), userId, payload); err != nil {
			return fmt.Errorf("dispatch pre-expanded keywords: %w", err)
		}
	} else if !status.ExpandedKeywordsDone {
		if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEGetExpandedUserKeywords), userId, payload); err != nil {
			return fmt.Errorf("dispatch expanded keywords: %w", err)
		}
	}

	return nil
}

func (s *seoBlogGeneratorSiteIntelligence) dispatchIncompletePostDataFetchSteps(
	ctx context.Context, userId string, wec *models.WebEntityContext, webEntityContextId string,
) error {
	dc := pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextId,
	}

	// Walk right-to-left: check the furthest stage first
	if wec.Status >= models.SIEStatusClusteringDone {
		return nil
	}

	if wec.Status >= models.SIEStatusOpportunityScoreCalculated {
		return s.pipeline.DispatchNext(ctx, sie.ProcessSIEOpportunityScore, dc)
	}

	if wec.Status >= models.SIEStatusFunnelClassificationDone {
		return s.pipeline.DispatchNext(ctx, sie.ProcessSIEFunnelClassification, dc)
	}

	if wec.Status >= models.SIEStatusPostProcessingDone {
		return s.pipeline.DispatchNext(ctx, sie.ProcessSIEPostProcessing, dc)
	}

	payload := pipeline.StandardPayload{
		WebEntityID:        wec.WebEntityID.Hex(),
		WebEntityContextID: webEntityContextId,
	}
	return s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEPostProcessing), userId, payload)
}
