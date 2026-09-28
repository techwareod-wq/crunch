package service

import (
	"strings"
	"testing"

	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/prompts"
)

// The clustering template gained conditional blocks (ClusterCountRule,
// UpgradeMerge, ScheduledKeywords); render all three shapes so a template
// syntax error fails here instead of mid-pipeline at runtime.
func TestKeywordClusteringPromptRenders(t *testing.T) {
	utils := llmutil.NewLlmUtils()
	base := sieDto.ClusturingPromptRequest{
		BusinessName:  "Acme",
		ProductType:   "CRM",
		Integrations:  "Sheets",
		KeywordsBatch: []sieDto.ClusturingKeyword{{SequenceID: 1, Keyword: "best crm"}},
	}

	t.Run("first run, trial count", func(t *testing.T) {
		dto := base
		dto.ClusterCountRule = clusterCountRule(3)
		out, err := utils.ConstructPrompt(prompts.KeywordClustering, dto)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(out, "Create exactly 3 clusters.") {
			t.Error("trial cluster-count rule missing from prompt")
		}
		if strings.Contains(out, "Existing clusters:") {
			t.Error("first run must not render the existing-clusters table")
		}
	})

	t.Run("re-cluster, full range", func(t *testing.T) {
		dto := base
		dto.ClusterCountRule = clusterCountRule(0)
		dto.ExistingClusters = []sieDto.ClusterSummaryForPrompt{
			{ClusterID: "crm-basics", ClusterName: "CRM Basics", PillarKeyword: "best crm", Examples: "crm tips"},
		}
		out, err := utils.ConstructPrompt(prompts.KeywordClustering, dto)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(out, "Create between 6 and 10 clusters.") {
			t.Error("full cluster-count rule missing")
		}
		if !strings.Contains(out, "crm-basics") {
			t.Error("existing cluster row missing")
		}
		if strings.Contains(out, "trial-to-paid upgrade merge") {
			t.Error("non-merge run must not render the upgrade-merge block")
		}
	})

	t.Run("upgrade merge", func(t *testing.T) {
		dto := base
		dto.ClusterCountRule = clusterCountRule(0)
		dto.UpgradeMerge = true
		dto.ScheduledKeywords = "best crm, crm pricing"
		dto.ExistingClusters = []sieDto.ClusterSummaryForPrompt{
			{ClusterID: "crm-basics", ClusterName: "CRM Basics", PillarKeyword: "best crm", Examples: "crm tips"},
		}
		out, err := utils.ConstructPrompt(prompts.KeywordClustering, dto)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(out, "trial-to-paid upgrade merge") {
			t.Error("upgrade-merge block missing")
		}
		if !strings.Contains(out, "best crm, crm pricing") {
			t.Error("scheduled-keywords freeze list missing")
		}
		if !strings.Contains(out, "never shrink") {
			t.Error("cluster-id preservation instruction missing")
		}
	})
}
