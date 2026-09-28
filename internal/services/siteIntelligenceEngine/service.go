package siteIntelligenceEngine

import (
	"context"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	ProcessSiteIntelligence              pipeline.ProcessType = "SITE_INTELLIGENCE_PROCESS"
	ProcessSIEGetUserKeywords            pipeline.ProcessType = "SIE_GET_USER_KEYWORDS"
	ProcessSIEGetCompetitorKeywords      pipeline.ProcessType = "SIE_GET_COMPETITOR_KEYWORDS"
	ProcessSIEGetPreExpandedUserKeywords pipeline.ProcessType = "SIE_GET_PRE_EXPANDED_USER_KEYWORDS"
	ProcessSIEGetExpandedUserKeywords    pipeline.ProcessType = "SIE_GET_EXPANDED_USER_KEYWORDS"
	ProcessSIEPostProcessing             pipeline.ProcessType = "SIE_POST_PROCESSING"
	ProcessSIEFunnelClassification       pipeline.ProcessType = "SIE_FUNNEL_CLASSIFICATION"
	ProcessSIEClustering                 pipeline.ProcessType = "SIE_CLUSTERING"
	ProcessSIEOpportunityScore           pipeline.ProcessType = "SIE_OPPORTUNITY_SCORE"
	ProcessSIEAddManualKeyword           pipeline.ProcessType = "SIE_ADD_MANUAL_KEYWORD"
	// ProcessSIEUpgradeExpand is the trial→paid upgrade kickoff: it rewinds the
	// WEC's pipeline cursor (sie_mode → full) and re-enters Orchestrate, so the
	// ORIGINAL async stages re-run at full limits on top of the trial data —
	// merging instead of wiping — before handing off to SE_EXTEND_SCHEDULE.
	// Dispatched by the payment webhook after a CAS claim on the WEC's
	// upgrade_state; resumable because the rewind CAS makes redeliveries
	// re-dispatch only the incomplete stages.
	ProcessSIEUpgradeExpand pipeline.ProcessType = "SIE_UPGRADE_EXPAND"
)

type SIEMetadata struct {
	WebEntityID        string
	WebEntityContextID string
	CompetitorURL      string
}

// FunnelClassificationPayload carries IDs into the keyword collection rather
// than full Keyword copies. The handler hydrates via a single $in fetch.
type FunnelClassificationPayload struct {
	WebEntityContextID string               `json:"webEntityContextId"`
	KeywordIDs         []primitive.ObjectID `json:"keywordIds"`
	IsRetry            bool                 `json:"isRetry"`
}

type ManualKeywordPayload struct {
	WebEntityContextID string             `json:"webEntityContextId"`
	KeywordID          primitive.ObjectID `json:"keywordId"`
}

type SiteIntelligenceService interface {
	Orchestrate(ctx context.Context, userId, webEntityId string) error
	GetUserKeywords(ctx context.Context, userId string, metadata SIEMetadata) error
	GetCompetitorKeywords(ctx context.Context, userId string, metadata SIEMetadata) error
	GetPreExpandedUserKeywords(ctx context.Context, userId string, metadata SIEMetadata) error
	GetExpandedUserKeywords(ctx context.Context, userId string, metadata SIEMetadata) error
	PostProcessing(ctx context.Context, userId string, metadata SIEMetadata) error
	ClassifyFunnelKeywords(ctx context.Context, userId string, payload FunnelClassificationPayload) error
	CalculateOpportunityScore(ctx context.Context, userId string, metadata SIEMetadata) error
	ClusterKeywords(ctx context.Context, userId string, metadata SIEMetadata) error
	DispatchFunnelClassification(ctx context.Context, userId, webEntityContextID string) error
	DispatchOpportunityScore(ctx context.Context, userId, webEntityContextID string) error
	DispatchClustering(ctx context.Context, userId, webEntityContextID string) error
	GetKeywordData(ctx context.Context, userId, webEntityId string) (*dto.KeywordDataResponse, error)
	GetClusterSupportingKeywords(ctx context.Context, userId, webEntityId string, clusterIndex, page int) (*dto.ClusterSupportingResponse, error)
	SearchKeywords(ctx context.Context, userId, webEntityId, query, status, usage string) (*dto.KeywordSearchResponse, error)
	AddManualKeyword(ctx context.Context, userId string, req dto.AddManualKeywordRequest) (*dto.AddManualKeywordResponse, error)
	HandleManualKeywordEnrichment(ctx context.Context, userId string, payload ManualKeywordPayload) error
	UpgradeExpand(ctx context.Context, userId string, metadata SIEMetadata) error
}
