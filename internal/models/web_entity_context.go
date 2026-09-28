package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const webEntityContextCollection = "webEntityContext"

// EnsureWebEntityContextIndexes creates the compound unique index on
// {user_id, web_entity_id}. This guarantees a single WebEntityContext per user
// per webentity so that concurrent step reads funneling through Orchestrate's
// find-then-create can't both create one (the dup-key loser re-reads). Idempotent.
func EnsureWebEntityContextIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(webEntityContextCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "web_entity_id", Value: 1},
		},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure web entity context indexes: %w", err)
	}
	return nil
}

const (
	// SIEStatusError is intentionally out-of-band (negative) instead of a value
	// inside the ordered progress sequence below. The pipeline's resume/gating
	// logic compares Status with >=, so an error sentinel wedged between two real
	// stages makes an errored run read as "between stage N and N+1" and corrupts
	// resume decisions. With Status == SIEStatusError the true progress is read
	// from ProcessMetadata.LastStatus instead — see (*WebEntityContext).EffectiveStatus.
	//
	// The real progress stages below are contiguous (0..9) so the ordered >=
	// comparisons read cleanly with no gaps to reason about.
	SIEStatusError                       = -1
	SIEStatusCreated                     = 0
	SIEStatusProcessing                  = 1
	SIEStatusPostProcessingStarted       = 2
	SIEStatusPostProcessingDone          = 3
	SIEStatusFunnelClassificationStarted = 4
	SIEStatusFunnelClassificationDone    = 5
	SIEStatusOpportunityScoreCalculated  = 6
	SIEStatusClusteringStarted           = 7
	SIEStatusClusteringDone              = 8
	SIEStatusSchedulingDone              = 9
)

// SIE pipeline modes. sie_mode is stamped ONCE at WEC creation from the
// user's entitlement status at that moment (trialing → trial); a missing
// field means "full" (every WEC created before trials existed). When the user
// subscribes, the upgrade kickoff flips the WEC to full and re-runs the base
// pipeline at full limits (see RewindWECForUpgradeRerun).
const (
	SIEModeTrial = "trial"
	SIEModeFull  = "full"
)

// Upgrade states. Empty (field absent) = never claimed; "expanding" = claimed
// by the webhook's CAS, the base pipeline is re-running at full limits on top
// of the trial data; "complete" = re-run finished, sie_mode flipped to full.
// The ""→"expanding" CAS is what makes Paddle webhook redeliveries no-op.
const (
	WECUpgradeStateExpanding = "expanding"
	WECUpgradeStateComplete  = "complete"
)

type SIEDataFetchStepStatus struct {
	UserKeywordsDone        bool `bson:"user_keywords_done"`
	CompetitorKeywordsDone  bool `bson:"competitor_keywords_done"`
	PreExpandedKeywordsDone bool `bson:"pre_expanded_keywords_done"`
	ExpandedKeywordsDone    bool `bson:"expanded_keywords_done"`
}

type SIEErrorData struct {
	Step    string `bson:"step" json:"step"`
	Message string `bson:"message" json:"message"`
}

func (s *SIEDataFetchStepStatus) AllDone() bool {
	if s == nil {
		return false
	}
	return s.UserKeywordsDone && s.CompetitorKeywordsDone && s.PreExpandedKeywordsDone && s.ExpandedKeywordsDone
}

type FunnelClassificationMetadata struct {
	FunnelClassificationTotal            int                  `bson:"funnel_classification_total"`
	FunnelClassificationProcessed        int                  `bson:"funnel_classification_processed"`
	FunnelClassificationFailed           int                  `bson:"funnel_classification_failed"`
	FunnelClassificationFailedKeywordIDs []primitive.ObjectID `bson:"funnel_classification_failed_keyword_ids,omitempty"`
}

type SchedulingMetadata struct {
	Total int `bson:"total"`
	// RerunKey is the queue MessageID of the SE_RERUN_SCHEDULE delivery whose
	// window insert last started. Together with RerunAnchor it lets an
	// error-retry of that same message resume filling its own window instead of
	// opening a second one off the moved calendar end. Overwritten by each
	// subsequent rerun.
	RerunKey string `bson:"rerun_key,omitempty"`
	// RerunAnchor is the first calendar day (00:00 UTC) of that rerun's window.
	RerunAnchor time.Time `bson:"rerun_anchor,omitempty"`
}

type ProcessMetadata struct {
	DataFetchStepStatus          *SIEDataFetchStepStatus      `bson:"data_fetch_step_status"`
	FunnelClassificationMetadata FunnelClassificationMetadata `bson:"funnel_classification_metadata"`
	SchedulingMetadata           SchedulingMetadata           `bson:"scheduling_metadata"`
	// LastStatus mirrors WebEntityContext.Status on every non-error transition,
	// recording the furthest pipeline stage the run actually reached. When Status
	// is later flipped to the out-of-band SIEStatusError sentinel this field still
	// identifies the exact stage, so resume recovers from there instead of
	// restarting the pipeline. Read it via (*WebEntityContext).EffectiveStatus.
	LastStatus int `bson:"last_status"`
}

// Cluster carries only ID references to keyword docs. Full keyword data is
// hydrated by the read path (e.g. GetKeywordData) via a single batched fetch.
// Sort order at write time is the responsibility of the caller (clustering
// stage), which has the opportunity-score data in hand.
type Cluster struct {
	ClusterID            string               `bson:"cluster_id" json:"cluster_id"`
	ClusterName          string               `bson:"cluster_name" json:"cluster_name"`
	PillarKeywordID      primitive.ObjectID   `bson:"pillar_keyword_id" json:"pillar_keyword_id"`
	PillarIntent         string               `bson:"pillar_intent" json:"pillar_intent"`
	PillarFunnel         FunnelStage          `bson:"pillar_funnel" json:"pillar_funnel"`
	PillarPublished      bool                 `bson:"pillar_published" json:"pillar_published"`
	SupportingKeywordIDs []primitive.ObjectID `bson:"supporting_keyword_ids" json:"supporting_keyword_ids"`
}

type WebEntityContext struct {
	ID                          primitive.ObjectID `bson:"_id,omitempty"`
	UserID                      primitive.ObjectID `bson:"user_id"`
	WebEntityID                 primitive.ObjectID `bson:"web_entity_id"`
	Keywords                    KeyWords           `bson:"keywords,omitempty"`
	Status                      int                `bson:"status"`
	ErrorData                   []SIEErrorData     `bson:"error_data,omitempty"`
	CompetitorKeywordsProcessed int                `bson:"competitor_keywords_processed"`
	TotalCompetitors            int                `bson:"total_competitors"`
	ProcessMetadata             ProcessMetadata    `bson:"process_metadata"`
	Clusters                    []Cluster          `bson:"clusters,omitempty"`
	// SIEMode selects trial-sized vs full pipeline limits. Stamped once at
	// creation; missing = full (pre-trial WECs). Read via EffectiveSIEMode.
	SIEMode string `bson:"sie_mode,omitempty"`
	// UpgradeState tracks the trial→paid upgrade re-run (see WECUpgradeState*).
	UpgradeState string    `bson:"upgrade_state,omitempty"`
	CreatedAt    time.Time `bson:"created_at,omitempty"`
	UpdatedAt    time.Time `bson:"updated_at,omitempty"`
}

// EffectiveSIEMode resolves the WEC's pipeline mode, defaulting the missing
// field (every WEC created before trial mode existed) to full.
func (w *WebEntityContext) EffectiveSIEMode() string {
	if w.SIEMode == SIEModeTrial {
		return SIEModeTrial
	}
	return SIEModeFull
}

// EffectiveStatus returns the pipeline's true progress stage. Status carries the
// live stage during a healthy run, but is overwritten with the out-of-band
// SIEStatusError sentinel on failure; in that case the last real stage is read
// from ProcessMetadata.LastStatus (which the error write deliberately leaves
// untouched). All resume/gating logic should compare against this, not Status.
func (w *WebEntityContext) EffectiveStatus() int {
	if w.Status == SIEStatusError {
		return w.ProcessMetadata.LastStatus
	}
	return w.Status
}

// KeyWords holds raw keyword pools collected during the data-fetch stage.
// Processed keywords no longer live here — they're persisted to the
// `keyword` collection and queried via web_entity_context_id.
type KeyWords struct {
	UserKeyWords        []Keyword `bson:"user_keywords,omitempty"`
	CompetitorKeyWords  []Keyword `bson:"competitor_keywords,omitempty"`
	PreExpandedKeyWords []string  `bson:"pre_expanded_keywords,omitempty"`
	ExpandedKeyWords    []Keyword `bson:"expanded_keywords,omitempty"`
}

func CreateWebEntityContext(ctx context.Context, webEntityContext *WebEntityContext) error {
	now := time.Now()
	webEntityContext.CreatedAt = now
	webEntityContext.UpdatedAt = now

	id, err := InsertOne(ctx, webEntityContextCollection, webEntityContext)
	if err != nil {
		return err
	}
	webEntityContext.ID = id
	return nil
}

func GetWebEntityContext(ctx context.Context, webEntityContextId string) (bool, *WebEntityContext, error) {
	objID, err := primitive.ObjectIDFromHex(webEntityContextId)
	if err != nil {
		return false, nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}

	var webEntityContext WebEntityContext
	isPresent, err := FindOne(ctx, webEntityContextCollection, bson.M{"_id": objID}, &webEntityContext)
	if err != nil {
		return false, nil, fmt.Errorf("failed to find web entity context by ID: %w", err)
	}

	return isPresent, &webEntityContext, nil
}

func GetWebEntityContextFromWebEntityAndUserID(ctx context.Context, webEntityId, userId string) (bool, *WebEntityContext, error) {
	webEntityOID, err := primitive.ObjectIDFromHex(webEntityId)
	if err != nil {
		return false, nil, fmt.Errorf("invalid web entity ID: %w", err)
	}
	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return false, nil, fmt.Errorf("invalid user ID: %w", err)
	}

	var webEntityContext WebEntityContext
	isPresent, err := FindOne(ctx, webEntityContextCollection, bson.M{"web_entity_id": webEntityOID, "user_id": userOID}, &webEntityContext)
	if err != nil {
		return false, nil, fmt.Errorf("failed to find web entity context by Webentity and user id: %w", err)
	}

	return isPresent, &webEntityContext, nil
}

func setWECFields(ctx context.Context, webEntityContextID string, fields bson.M) error {
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	fields["updated_at"] = time.Now()
	return UpdateOne(ctx, webEntityContextCollection, bson.M{"_id": objID}, bson.M{"$set": fields})
}

func updateWECRaw(ctx context.Context, webEntityContextID string, update bson.M) error {
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	if setFields, ok := update["$set"].(bson.M); ok {
		setFields["updated_at"] = time.Now()
	} else {
		update["$set"] = bson.M{"updated_at": time.Now()}
	}
	return UpdateOne(ctx, webEntityContextCollection, bson.M{"_id": objID}, update)
}

// WECUpdateReq is the request struct for partial updates to WebEntityContext.
// Pointer fields allow distinguishing "not provided" from zero values.
type WECUpdateReq struct {
	Keywords                    *KeyWords
	Status                      *int
	ErrorData                   []SIEErrorData
	CompetitorKeywordsProcessed *int
	TotalCompetitors            *int
	ProcessMetadata             *ProcessMetadata
	Clusters                    []Cluster
}

func UpdateWebEntityContext(ctx context.Context, webEntityContextID string, req WECUpdateReq) error {
	update := bson.M{}

	if req.Keywords != nil {
		kw := req.Keywords
		if kw.UserKeyWords != nil {
			update["keywords.user_keywords"] = kw.UserKeyWords
		}
		if kw.CompetitorKeyWords != nil {
			update["keywords.competitor_keywords"] = kw.CompetitorKeyWords
		}
		if kw.PreExpandedKeyWords != nil {
			update["keywords.pre_expanded_keywords"] = kw.PreExpandedKeyWords
		}
		if kw.ExpandedKeyWords != nil {
			update["keywords.expanded_keywords"] = kw.ExpandedKeyWords
		}
	}

	// Status (pointer so zero value SIEStatusCreated works)
	if req.Status != nil {
		update["status"] = *req.Status
		// Mirror genuine progress into process metadata so it survives a later
		// flip to the SIEStatusError sentinel (see EffectiveStatus). The error
		// transition goes through AppendErrorData, never this path, so this only
		// ever records real pipeline stages.
		if *req.Status != SIEStatusError {
			update["process_metadata.last_status"] = *req.Status
		}
	}

	// Error data (empty slice clears, nil means "don't touch")
	if req.ErrorData != nil {
		update["error_data"] = req.ErrorData
	}

	if req.CompetitorKeywordsProcessed != nil {
		update["competitor_keywords_processed"] = *req.CompetitorKeywordsProcessed
	}
	if req.TotalCompetitors != nil {
		update["total_competitors"] = *req.TotalCompetitors
	}

	if req.ProcessMetadata != nil {
		pm := req.ProcessMetadata
		if pm.DataFetchStepStatus != nil {
			s := pm.DataFetchStepStatus
			if s.UserKeywordsDone {
				update["process_metadata.data_fetch_step_status.user_keywords_done"] = true
			}
			if s.CompetitorKeywordsDone {
				update["process_metadata.data_fetch_step_status.competitor_keywords_done"] = true
			}
			if s.PreExpandedKeywordsDone {
				update["process_metadata.data_fetch_step_status.pre_expanded_keywords_done"] = true
			}
			if s.ExpandedKeywordsDone {
				update["process_metadata.data_fetch_step_status.expanded_keywords_done"] = true
			}
		}
		fc := pm.FunnelClassificationMetadata
		if fc.FunnelClassificationTotal != 0 {
			update["process_metadata.funnel_classification_metadata.funnel_classification_total"] = fc.FunnelClassificationTotal
		}
		if fc.FunnelClassificationProcessed != 0 {
			update["process_metadata.funnel_classification_metadata.funnel_classification_processed"] = fc.FunnelClassificationProcessed
		}
		if fc.FunnelClassificationFailed != 0 {
			update["process_metadata.funnel_classification_metadata.funnel_classification_failed"] = fc.FunnelClassificationFailed
		}
		if fc.FunnelClassificationFailedKeywordIDs != nil {
			update["process_metadata.funnel_classification_metadata.funnel_classification_failed_keyword_ids"] = fc.FunnelClassificationFailedKeywordIDs
		}
	}

	// Clusters. Ordering is the caller's responsibility — Cluster docs hold
	// only ID references, so sorting by opportunity score has to happen at
	// the clustering stage where the hydrated data is in scope.
	if req.Clusters != nil {
		update["clusters"] = req.Clusters
	}

	if len(update) == 0 {
		return nil
	}

	return setWECFields(ctx, webEntityContextID, update)
}

// AppendCluster appends a new cluster to the WebEntityContext's clusters array.
// The caller is responsible for checking that a cluster with the same
// cluster_id does not already exist (the manual-keyword service short-circuits
// a slug collision into an assign-existing path), so this is a plain $push.
func AppendCluster(ctx context.Context, webEntityContextID string, c Cluster) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$push": bson.M{"clusters": c},
	})
}

// AppendKeywordIDToCluster adds a keyword ID to the supporting_keyword_ids of
// the matching cluster. Uses $addToSet so SQS at-least-once redelivery cannot
// double-insert the same keyword ID.
func AppendKeywordIDToCluster(ctx context.Context, webEntityContextID, clusterID string, keywordID primitive.ObjectID) error {
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"c.cluster_id": clusterID}},
	})
	_, err = Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{"_id": objID},
		bson.M{
			"$addToSet": bson.M{"clusters.$[c].supporting_keyword_ids": keywordID},
			"$set":      bson.M{"updated_at": time.Now()},
		},
		opts,
	)
	if err != nil {
		return fmt.Errorf("append keyword %s to cluster %s: %w", keywordID.Hex(), clusterID, err)
	}
	return nil
}

// RemoveKeywordIDsFromClusters pulls the given keyword IDs out of every
// cluster's supporting_keyword_ids array so deleted keyword docs leave no
// dangling references. Pillar references are not touched — pillars are
// protected from dedupe deletion upstream.
func RemoveKeywordIDsFromClusters(ctx context.Context, webEntityContextID string, ids []primitive.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	// The $exists guard makes this a no-op (matched 0) on WECs that never
	// reached clustering — the all-positional $[] errors on a missing path.
	_, err = Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{"_id": objID, "clusters": bson.M{"$exists": true}},
		bson.M{
			"$pull": bson.M{"clusters.$[].supporting_keyword_ids": bson.M{"$in": ids}},
			"$set":  bson.M{"updated_at": time.Now()},
		},
	)
	if err != nil {
		return fmt.Errorf("remove keyword ids from clusters for WEC %s: %w", webEntityContextID, err)
	}
	return nil
}

func AppendCompetitorKeywords(ctx context.Context, webEntityContextID string, keywords []Keyword) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$push": bson.M{
			"keywords.competitor_keywords": bson.M{"$each": keywords},
		},
		"$inc": bson.M{
			"competitor_keywords_processed": 1,
		},
	})
}

func GetSIEDataFetchStepStatus(ctx context.Context, webEntityContextID string) (*SIEDataFetchStepStatus, error) {
	ok, wec, err := GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}
	return wec.ProcessMetadata.DataFetchStepStatus, nil
}

func SetStatusIfCurrent(ctx context.Context, webEntityContextID string, currentStatus, newStatus int) error {
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	set := bson.M{
		"status":     newStatus,
		"updated_at": time.Now(),
	}
	// Keep the progress snapshot in lockstep with Status (see EffectiveStatus).
	if newStatus != SIEStatusError {
		set["process_metadata.last_status"] = newStatus
	}
	return UpdateOne(ctx, webEntityContextCollection,
		bson.M{"_id": objID, "status": currentStatus},
		bson.M{"$set": set},
	)
}

// TryAdvanceStatus atomically moves a WEC's status from any of fromStatuses to
// toStatus in a single conditional UpdateOne, returning whether THIS call won
// the transition. It is the concurrency primitive behind the pipeline's
// exactly-once stage hand-off: a worker claims a stage (predecessor → …Started)
// and later completes it (…Started → …Done), and only the worker that observes
// claimed == true performs the side effect / DispatchNext. Unlike
// SetStatusIfCurrent it does NOT error when no document matches — a loser simply
// gets claimed == false and exits cleanly (db.UpdateOne treats MatchedCount==0
// as an error, which is wrong for a CAS where losing is expected).
//
// fromStatuses commonly includes both the real predecessor and the stage's own
// …Started value (so a crashed claim can re-run, P6) and SIEStatusError (so a
// retry whose prior attempt flipped status to the error sentinel can still
// re-claim, mirroring computeResetStatus). last_status is kept in lockstep for
// every non-error transition so EffectiveStatus stays correct.
func TryAdvanceStatus(ctx context.Context, webEntityContextID string, fromStatuses []int, toStatus int) (bool, error) {
	objID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return false, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	set := bson.M{
		"status":     toStatus,
		"updated_at": time.Now(),
	}
	// Keep the progress snapshot in lockstep with Status (see EffectiveStatus).
	if toStatus != SIEStatusError {
		set["process_metadata.last_status"] = toStatus
	}
	res, err := Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{"_id": objID, "status": bson.M{"$in": fromStatuses}},
		bson.M{"$set": set},
	)
	if err != nil {
		return false, fmt.Errorf("try advance status for WEC %s: %w", webEntityContextID, err)
	}
	return res.MatchedCount == 1, nil
}

// AppendErrorData records a hard failure and flips Status to the out-of-band
// SIEStatusError sentinel. It deliberately does NOT touch
// process_metadata.last_status, so the furthest stage reached is preserved for
// resume (see EffectiveStatus / computeResetStatus).
func AppendErrorData(ctx context.Context, webEntityContextID string, errorData SIEErrorData) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$push": bson.M{"error_data": errorData},
		"$set": bson.M{
			"status": SIEStatusError,
		},
	})
}

// AppendErrorDiagnostic records an error entry on the WEC without flipping
// status to SIEStatusError. Use this for soft/partial failures the pipeline
// intentionally proceeds past (e.g. a single competitor's keyword fetch
// failing, or an LLM hallucinating a sequence_id) where the run should not be
// treated as errored/retriable. Use AppendErrorData when the step itself fails.
func AppendErrorDiagnostic(ctx context.Context, webEntityContextID string, errorData SIEErrorData) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$push": bson.M{"error_data": errorData},
	})
}

// SetFunnelProgressCounts overwrites the WEC's funnel display counters with
// authoritative, recomputed values via $set (idempotent). Used by the
// data-repair migration to correct counters the old $inc inflated past total.
func SetFunnelProgressCounts(ctx context.Context, webEntityContextID string, processed, failed, total int) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$set": bson.M{
			"process_metadata.funnel_classification_metadata.funnel_classification_processed": processed,
			"process_metadata.funnel_classification_metadata.funnel_classification_failed":    failed,
			"process_metadata.funnel_classification_metadata.funnel_classification_total":     total,
		},
	})
}

// IncrementFunnelProgress atomically increments the processed and/or failed
// counters and optionally appends failed keyword IDs (references into the
// `keyword` collection).
//
// Deprecated: the funnel completion gate is now derived from authoritative
// keyword state (CountFunnelSettledKeywords), not these counters. The $inc was
// not idempotent under at-least-once delivery and inflated processed/failed past
// total on every chunk redelivery. Per-keyword success/failure is now recorded
// idempotently (UpdateKeywordFunnels / MarkKeywordsFunnelFailed). Retained only
// so the data-repair migration and any external callers keep compiling.
func IncrementFunnelProgress(ctx context.Context, webEntityContextID string, processed, failed int, failedKeywordIDs []primitive.ObjectID) error {
	update := bson.M{
		"$inc": bson.M{
			"process_metadata.funnel_classification_metadata.funnel_classification_processed": processed,
			"process_metadata.funnel_classification_metadata.funnel_classification_failed":    failed,
		},
	}
	if len(failedKeywordIDs) > 0 {
		update["$push"] = bson.M{
			"process_metadata.funnel_classification_metadata.funnel_classification_failed_keyword_ids": bson.M{"$each": failedKeywordIDs},
		}
	}
	return updateWECRaw(ctx, webEntityContextID, update)
}

// SetSchedulingTotal stamps the planned article count for a WEC. Called once
// by SE Orchestrate after persisting the calendar.
func SetSchedulingTotal(ctx context.Context, webEntityContextID string, total int) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$set": bson.M{"process_metadata.scheduling_metadata.total": total},
	})
}

// SetSchedulingRerunMarker stamps the rerun resume marker (see
// SchedulingMetadata.RerunKey). Written by SE RerunSchedule immediately before
// it inserts a window's rows.
func SetSchedulingRerunMarker(ctx context.Context, webEntityContextID, rerunKey string, anchor time.Time) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$set": bson.M{
			"process_metadata.scheduling_metadata.rerun_key":    rerunKey,
			"process_metadata.scheduling_metadata.rerun_anchor": anchor,
		},
	})
}

// FindTrialModeWECsForUser returns the user's WECs still in trial mode — the
// webhook's expand-trigger scan. In practice one per user.
func FindTrialModeWECsForUser(ctx context.Context, userID primitive.ObjectID) ([]WebEntityContext, error) {
	cur, err := Collection(webEntityContextCollection).Find(ctx, bson.M{
		"user_id":  userID,
		"sie_mode": SIEModeTrial,
	})
	if err != nil {
		return nil, fmt.Errorf("find trial-mode WECs for user %s: %w", userID.Hex(), err)
	}
	var out []WebEntityContext
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode trial-mode WECs for user %s: %w", userID.Hex(), err)
	}
	return out, nil
}

// ClaimWECUpgrade atomically claims the expand pipeline for a WEC: upgrade
// state absent/"" → "expanding". Returns whether THIS call won the claim —
// a Paddle redelivery (or a second activated event) loses the CAS and no-ops.
func ClaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) (bool, error) {
	res, err := Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{"_id": wecID, "upgrade_state": bson.M{"$in": bson.A{"", nil}}},
		bson.M{"$set": bson.M{
			"upgrade_state": WECUpgradeStateExpanding,
			"updated_at":    time.Now(),
		}},
	)
	if err != nil {
		return false, fmt.Errorf("claim upgrade for WEC %s: %w", wecID.Hex(), err)
	}
	return res.MatchedCount == 1, nil
}

// UnclaimWECUpgrade rolls a claim back to unclaimed after the follow-up
// dispatch failed — otherwise the CAS would make every webhook redelivery
// no-op against a claim that has no kickoff message behind it. Guarded on
// state=="expanding" and sie_mode still trial (the kickoff's rewind is the
// first thing the pipeline does), so it can never undo a claim whose re-run
// actually started.
func UnclaimWECUpgrade(ctx context.Context, wecID primitive.ObjectID) error {
	_, err := Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{
			"_id":           wecID,
			"upgrade_state": WECUpgradeStateExpanding,
			"sie_mode":      SIEModeTrial,
		},
		bson.M{
			"$unset": bson.M{"upgrade_state": ""},
			"$set":   bson.M{"updated_at": time.Now()},
		},
	)
	if err != nil {
		return fmt.Errorf("unclaim upgrade for WEC %s: %w", wecID.Hex(), err)
	}
	return nil
}

// RewindWECForUpgradeRerun flips a claimed trial WEC to full mode and rewinds
// the base pipeline cursor to the start so the standard SIE stages re-run at
// full limits ON TOP of the trial data — every existing keyword doc, cluster,
// and scheduled row is left untouched; the stages themselves merge rather than
// wipe when prior data exists. keepSeeds preserves the trial's pre-expanded
// seed list (data_fetch flag stays done) so the expansion fetch reuses it
// instead of regenerating.
//
// CAS on sie_mode=trial + upgrade_state=expanding: exactly one kickoff
// performs the rewind; a redelivered kickoff observes sie_mode=full, skips the
// rewind, and just re-dispatches the incomplete stages (resume semantics).
func RewindWECForUpgradeRerun(ctx context.Context, wecID primitive.ObjectID, keepSeeds bool) (bool, error) {
	res, err := Collection(webEntityContextCollection).UpdateOne(ctx,
		bson.M{
			"_id":           wecID,
			"sie_mode":      SIEModeTrial,
			"upgrade_state": WECUpgradeStateExpanding,
		},
		bson.M{"$set": bson.M{
			"sie_mode":                     SIEModeFull,
			"status":                       SIEStatusProcessing,
			"process_metadata.last_status": SIEStatusProcessing,
			"process_metadata.data_fetch_step_status": SIEDataFetchStepStatus{
				PreExpandedKeywordsDone: keepSeeds,
			},
			"updated_at": time.Now(),
		}},
	)
	if err != nil {
		return false, fmt.Errorf("rewind WEC %s for upgrade re-run: %w", wecID.Hex(), err)
	}
	return res.MatchedCount == 1, nil
}

// FinalizeWECUpgrade marks the upgrade re-run complete. sie_mode is set
// defensively (the rewind already flipped it) so a legacy claim can never
// finalize while still reading as trial. Idempotent.
func FinalizeWECUpgrade(ctx context.Context, webEntityContextID string) error {
	return updateWECRaw(ctx, webEntityContextID, bson.M{
		"$set": bson.M{
			"sie_mode":      SIEModeFull,
			"upgrade_state": WECUpgradeStateComplete,
		},
	})
}

func GetProcessMetadata(ctx context.Context, webEntityContextID string) (*ProcessMetadata, error) {
	ok, wec, err := GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}
	return &wec.ProcessMetadata, nil
}

// FindWebEntityContextIDsByUserID returns the _ids of every WEC the user owns.
// The admin deletion cascade captures these BEFORE deleting anything: keyword
// docs carry no user_id, so this list is the only way to reach them.
func FindWebEntityContextIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	cur, err := Collection(webEntityContextCollection).Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var rows []struct {
		ID primitive.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// FindWebEntityContextIDsByWebEntityID returns the _ids of every WEC attached
// to a web entity. The analytics engine's scoping hop: keywords and master
// contexts hang off WECs, not the entity itself.
func FindWebEntityContextIDsByWebEntityID(ctx context.Context, webEntityID primitive.ObjectID) ([]primitive.ObjectID, error) {
	cur, err := Collection(webEntityContextCollection).Find(ctx,
		bson.M{"web_entity_id": webEntityID},
		options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var rows []struct {
		ID primitive.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// DeleteWebEntityContextsByUserID removes every WEC the user owns. Admin
// deletion cascade only — run after keywords/articles/master contexts are gone
// (children first). Zero matches is a no-op so re-runs converge.
func DeleteWebEntityContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(webEntityContextCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
