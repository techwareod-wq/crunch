package dto

import (
	"github.com/atharva-ng/crunch/internal/models"
)

type ExpandKeywordsPromptRequest struct {
	BusinessName      string `json:"BusinessName" validate:"required"`
	ProductType       string `json:"ProductType" validate:"required"`
	PrimaryUseCase    string `json:"PrimaryUseCase"`
	KeyFeatures       string `json:"KeyFeatures"`
	KeyDifferentiator string `json:"KeyDifferentiator" validate:"required"`
	Integrations      string `json:"Integrations" validate:"required"`
	IcpRoles          string `json:"IcpRoles" validate:"required"`
	IcpPains          string `json:"IcpPains" validate:"required"`
	Competitors       string `json:"Competitors"`
}

type FunnelClassificationPromptRequest struct {
	BusinessName  string           `json:"BusinessName" validate:"required"`
	ProductType   string           `json:"ProductType" validate:"required"`
	KeywordsBatch []models.Keyword `json:"KeywordsBatch" validate:"required"`
}

type ClusturingKeyword struct {
	SequenceID int    `json:"sequence_id"`
	Keyword    string `json:"keyword"`
}

type ClusturingPromptRequest struct {
	BusinessName  string              `json:"BusinessName" validate:"required"`
	ProductType   string              `json:"ProductType" validate:"required"`
	Integrations  string              `json:"Integrations" validate:"required"`
	KeywordsBatch []ClusturingKeyword `json:"KeywordsBatch" validate:"required"`
	// ExistingClusters carries the cluster map from a prior clustering run (empty
	// on the first run). When present it is rendered into the prompt so the model
	// reuses the same cluster_id / cluster_name instead of inventing a fresh map,
	// preserving prior work (e.g. published pillars) across re-clustering.
	ExistingClusters []ClusterSummaryForPrompt `json:"ExistingClusters"`
	// ClusterCountRule is the rendered cluster-count instruction: trial runs get
	// an exact count ("Create exactly 3 clusters."), full runs the 6–10 range.
	ClusterCountRule string `json:"ClusterCountRule" validate:"required"`
	// UpgradeMerge switches the prompt into trial→paid expand semantics: grow
	// the existing cluster map instead of rebuilding it — existing cluster_ids
	// must survive, and keywords listed in ScheduledKeywords should keep their
	// current cluster so the already-published week-1 calendar stays coherent.
	UpgradeMerge bool `json:"UpgradeMerge"`
	// ScheduledKeywords is the comma-joined list of keyword texts that already
	// have calendar slots (only rendered when UpgradeMerge).
	ScheduledKeywords string `json:"ScheduledKeywords"`
}

type LLMCluster struct {
	ClusterID          string `json:"cluster_id"`
	ClusterName        string `json:"cluster_name"`
	PillarKeyword      string `json:"pillar_keyword"`
	PillarIntent       string `json:"pillar_intent"`
	PillarFunnel       string `json:"pillar_funnel"`
	SupportingKeywords []int  `json:"supporting_keywords"`
}

type ClusteringResponse struct {
	Clusters []LLMCluster `json:"clusters"`
}

// ClusterSummaryForPrompt is one row of the existing-clusters table fed to the
// manual-keyword cluster-assignment prompt.
type ClusterSummaryForPrompt struct {
	ClusterID     string `json:"cluster_id"`
	ClusterName   string `json:"cluster_name"`
	PillarKeyword string `json:"pillar_keyword"`
	// Examples is a comma-joined sample of supporting keyword texts.
	Examples string `json:"examples"`
}

// ManualKeywordClusterPromptRequest templates the cluster-assignment prompt for
// a single manually-added keyword against the WEC's existing clusters.
type ManualKeywordClusterPromptRequest struct {
	BusinessName      string                    `json:"BusinessName" validate:"required"`
	ProductType       string                    `json:"ProductType" validate:"required"`
	Keyword           string                    `json:"Keyword" validate:"required"`
	Volume            int                       `json:"Volume"`
	KeywordDifficulty int                       `json:"KeywordDifficulty"`
	Intent            string                    `json:"Intent"`
	Funnel            string                    `json:"Funnel"`
	Clusters          []ClusterSummaryForPrompt `json:"Clusters"`
}

// ManualKeywordClusterResponse is the LLM's cluster-assignment decision for a
// manually-added keyword.
type ManualKeywordClusterResponse struct {
	Action       string `json:"action"`
	ClusterID    string `json:"cluster_id"`
	ClusterName  string `json:"cluster_name"`
	IsNewCluster bool   `json:"is_new_cluster"`
	Reasoning    string `json:"reasoning"`
}
