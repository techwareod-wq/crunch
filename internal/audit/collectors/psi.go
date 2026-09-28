package collectors

import (
	"context"
	"fmt"
	"sort"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
)

// psiCollector runs ONE mobile PageSpeed Insights call on the homepage
// (decision 6): CrUX origin field data is the Performance scoring input; the
// Lighthouse lab score and opportunities ride along as finding detail.
// Non-critical: PSI being down renormalizes Performance away (§8.3).
type psiCollector struct{}

func (psiCollector) ID() core.CollectorID            { return CollectorPSI }
func (psiCollector) Produces() []core.Kind           { return []core.Kind{artifacts.KindPSI} }
func (psiCollector) AppliesTo(*models.AuditRun) bool { return true }
func (psiCollector) Critical() bool                  { return false }

func (psiCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	artifact, err := BuildPSIArtifact(ctx, deps, run)
	if err != nil {
		return err
	}
	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindPSI, artifact)
}

// BuildPSIArtifact runs the one mobile PSI call and normalizes the response
// WITHOUT touching the blackboard — shared by the collect stage (which
// upserts) and the re-check's scoped collection (which keeps the fresh
// artifact in memory; the stored run must never mutate).
func BuildPSIArtifact(ctx context.Context, deps Deps, run *models.AuditRun) (*artifacts.PSIArtifact, error) {
	resp, err := deps.PSI.RunPagespeed(ctx, dto.PagespeedRequest{
		URL:      run.TargetURL,
		Strategy: "mobile",
	})
	if err != nil {
		return nil, fmt.Errorf("psi collector: %w", err)
	}

	artifact := &artifacts.PSIArtifact{}
	if origin := resp.OriginLoadingExperience; origin != nil && len(origin.Metrics) > 0 {
		if lcp, ok := origin.Metrics[dto.PSIMetricLCP]; ok {
			artifact.HasFieldData = true
			artifact.OriginLCPMs = lcp.Percentile
		}
		if inp, ok := origin.Metrics[dto.PSIMetricINP]; ok {
			artifact.HasFieldData = true
			artifact.OriginINPMs = inp.Percentile
		}
		if cls, ok := origin.Metrics[dto.PSIMetricCLS]; ok {
			artifact.HasFieldData = true
			artifact.OriginCLS = cls.Percentile / 100 // CrUX reports CLS ×100
		}
	}
	if page := resp.LoadingExperience; page != nil {
		if lcp, ok := page.Metrics[dto.PSIMetricLCP]; ok {
			artifact.PageLCPMs = lcp.Percentile
		}
		if inp, ok := page.Metrics[dto.PSIMetricINP]; ok {
			artifact.PageINPMs = inp.Percentile
		}
		if cls, ok := page.Metrics[dto.PSIMetricCLS]; ok {
			artifact.PageCLS = cls.Percentile / 100
		}
	}
	if lh := resp.LighthouseResult; lh != nil {
		if lh.Categories != nil && lh.Categories.Performance != nil {
			artifact.LabPerformanceScore = lh.Categories.Performance.Score * 100
		}
		for _, audit := range lh.Audits {
			if audit.Details == nil || audit.Details.Type != "opportunity" || audit.Details.OverallSavingsMs <= 0 {
				continue
			}
			artifact.Opportunities = append(artifact.Opportunities, artifacts.PSIOpportunity{
				ID:        audit.ID,
				Title:     audit.Title,
				SavingsMs: audit.Details.OverallSavingsMs,
			})
		}
		sort.SliceStable(artifact.Opportunities, func(i, j int) bool {
			return artifact.Opportunities[i].SavingsMs > artifact.Opportunities[j].SavingsMs
		})
		if len(artifact.Opportunities) > 10 {
			artifact.Opportunities = artifact.Opportunities[:10]
		}
	}

	return artifact, nil
}
