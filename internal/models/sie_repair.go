package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WECRepairReport summarises one WebEntityContext's data-repair outcome. It is
// returned for every scanned WEC (including no-ops) so a dry-run can show the
// full picture before anything is written.
type WECRepairReport struct {
	WebEntityContextID string

	OldProcessed    int
	OldFailed       int
	OldTotal        int
	NewProcessed    int
	NewFailed       int
	NewTotal        int
	CountersChanged bool

	OldStatus     int
	NewStatus     int
	StatusChanged bool
}

// deriveArtifactStatus returns the furthest pipeline stage a WEC's persisted
// artifacts prove it reached. It is intentionally monotonic and conservative:
// clusters are the strongest signal, then opportunity scores, then a fully
// settled funnel, then merely having processed keywords. SchedulingDone leaves
// no SIE-owned artifact, so it is never inferred here (and the advance-only
// reconcile never rewinds a WEC already at SchedulingDone).
func deriveArtifactStatus(hasClusters bool, scored, settled, total int) int {
	switch {
	case hasClusters:
		return SIEStatusClusteringDone
	case scored > 0:
		return SIEStatusOpportunityScoreCalculated
	case total > 0 && settled >= total:
		return SIEStatusFunnelClassificationDone
	case total > 0:
		return SIEStatusPostProcessingDone
	default:
		return SIEStatusCreated
	}
}

// RepairWebEntityContexts walks every WebEntityContext and brings its persisted
// bookkeeping back in line with authoritative keyword state. For each WEC it:
//
//  1. Recomputes the funnel display counters (processed/failed/total) from the
//     keyword collection, overwriting values the old non-idempotent $inc inflated
//     past total.
//  2. Reconciles status FORWARD to the furthest stage its artifacts prove it
//     reached (see deriveArtifactStatus). Status is only ever advanced, never
//     rewound, so a WEC already at SchedulingDone is left alone.
//
// Errored WECs (Status == SIEStatusError) are skipped entirely so the existing
// resume path (Orchestrate/computeResetStatus) stays authoritative for them.
//
// With dryRun == true nothing is written; the returned reports still describe
// exactly what would change.
func RepairWebEntityContexts(ctx context.Context, dryRun bool) ([]WECRepairReport, error) {
	cursor, err := Collection(webEntityContextCollection).Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("list web entity contexts: %w", err)
	}
	defer cursor.Close(ctx)

	var reports []WECRepairReport
	for cursor.Next(ctx) {
		var wec WebEntityContext
		if err := cursor.Decode(&wec); err != nil {
			return reports, fmt.Errorf("decode web entity context: %w", err)
		}

		report, err := repairOneWEC(ctx, &wec, dryRun)
		if err != nil {
			return reports, fmt.Errorf("repair WEC %s: %w", wec.ID.Hex(), err)
		}
		reports = append(reports, report)
	}
	if err := cursor.Err(); err != nil {
		return reports, fmt.Errorf("cursor error: %w", err)
	}

	return reports, nil
}

func repairOneWEC(ctx context.Context, wec *WebEntityContext, dryRun bool) (WECRepairReport, error) {
	wecID := wec.ID.Hex()
	fc := wec.ProcessMetadata.FunnelClassificationMetadata

	report := WECRepairReport{
		WebEntityContextID: wecID,
		OldProcessed:       fc.FunnelClassificationProcessed,
		OldFailed:          fc.FunnelClassificationFailed,
		OldTotal:           fc.FunnelClassificationTotal,
		OldStatus:          wec.Status,
		NewStatus:          wec.Status,
	}

	classified, failed, total, err := CountKeywordsFunnelState(ctx, wecID)
	if err != nil {
		return report, err
	}
	report.NewProcessed = classified
	report.NewFailed = failed
	report.NewTotal = total
	report.CountersChanged = classified != report.OldProcessed ||
		failed != report.OldFailed ||
		total != report.OldTotal

	// Skip status reconciliation for errored WECs — the resume path owns them.
	if wec.Status != SIEStatusError {
		scored, err := countScoredKeywords(ctx, wecID)
		if err != nil {
			return report, err
		}
		settled := classified + failed
		artifactStatus := deriveArtifactStatus(len(wec.Clusters) > 0, scored, settled, total)
		if artifactStatus > wec.Status {
			report.NewStatus = artifactStatus
			report.StatusChanged = true
		}
	}

	if dryRun || (!report.CountersChanged && !report.StatusChanged) {
		return report, nil
	}

	if report.CountersChanged {
		if err := SetFunnelProgressCounts(ctx, wecID, classified, failed, total); err != nil {
			return report, err
		}
	}
	if report.StatusChanged {
		if err := setWECFields(ctx, wecID, bson.M{
			"status":                       report.NewStatus,
			"process_metadata.last_status": report.NewStatus,
		}); err != nil {
			return report, err
		}
	}

	return report, nil
}

// countScoredKeywords counts a WEC's bulk keywords that carry an opportunity
// score. Scores are written for the whole set in one BulkWrite, so a non-zero
// count is proof the opportunity-score stage ran.
func countScoredKeywords(ctx context.Context, webEntityContextID string) (int, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	count, err := Collection(keywordCollection).CountDocuments(ctx, bson.M{
		"web_entity_context_id": wecOID,
		"manually_added":        bson.M{"$ne": true},
		"opportunity_score":     bson.M{"$gt": 0},
	})
	if err != nil {
		return 0, fmt.Errorf("count scored keywords for WEC %s: %w", webEntityContextID, err)
	}
	return int(count), nil
}
