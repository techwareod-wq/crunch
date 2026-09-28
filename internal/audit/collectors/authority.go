package collectors

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// authorityCollector fetches the backlinks summary (the FetchDomainRating
// task shape — rank_scale one_hundred stays the single source of truth for
// the 0-100 rating) plus the Labs domain_rank_overview (decision 7).
// Non-critical.
type authorityCollector struct{}

func (authorityCollector) ID() core.CollectorID            { return CollectorAuthority }
func (authorityCollector) Produces() []core.Kind           { return []core.Kind{artifacts.KindAuthority} }
func (authorityCollector) AppliesTo(*models.AuditRun) bool { return true }
func (authorityCollector) Critical() bool                  { return false }

func (authorityCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	artifact, err := BuildAuthorityArtifact(ctx, deps, run)
	if err != nil {
		return err
	}
	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindAuthority, artifact)
}

// BuildAuthorityArtifact fetches the live backlinks summary + rank overview
// WITHOUT touching the blackboard — shared by the collect stage (which
// upserts) and the re-check's scoped collection (in-memory only).
func BuildAuthorityArtifact(ctx context.Context, deps Deps, run *models.AuditRun) (*artifacts.AuthorityArtifact, error) {
	artifact := &artifacts.AuthorityArtifact{}

	// Backlinks summary — the exact FetchDomainRating task shape, read wider
	// (profile fields are additive on the same response).
	summaryResp, summaryErr := deps.DFS.GetBacklinksSummary(ctx, dto.BacklinksSummaryRequest{Tasks: []dto.BacklinksSummaryTask{{
		Target:              run.TargetDomain,
		InternalListLimit:   10,
		IncludeSubdomains:   true,
		BacklinksFilters:    []interface{}{"dofollow", "=", true},
		BacklinksStatusType: "all",
		RankScale:           "one_hundred",
	}}})
	if summaryErr == nil && len(summaryResp.Tasks) > 0 && len(summaryResp.Tasks[0].Result) > 0 {
		r := summaryResp.Tasks[0].Result[0]
		artifact.HasBacklinkData = true
		artifact.DomainRating = r.Rank
		artifact.Backlinks = r.Backlinks
		artifact.BrokenBacklinks = r.BrokenBacklinks
		artifact.ReferringDomains = r.ReferringDomains
		artifact.ReferringMainDoms = r.ReferringMainDomains
		artifact.NofollowRefDomains = r.ReferringDomainsNofollow
	} else if summaryErr != nil {
		log.Warn("audit authority: backlinks summary failed", "domain", run.TargetDomain, "error", summaryErr)
	}

	// Labs rank overview (Domain Overview strip data) — queried against the
	// run's resolved market; Labs data is per-location, so the wrong country
	// reads as "no rankings" regardless of reality.
	artifact.LocationCode = RunLocationCode(run)
	overviewResp, overviewErr := deps.DFS.GetDomainRankOverview(ctx, dto.DomainRankOverviewRequest{Tasks: []dto.DomainRankOverviewTask{{
		Target:       run.TargetDomain,
		LocationCode: artifact.LocationCode,
		LanguageName: auditLanguageName,
	}}})
	if overviewErr == nil && len(overviewResp.Tasks) > 0 && len(overviewResp.Tasks[0].Result) > 0 &&
		len(overviewResp.Tasks[0].Result[0].Items) > 0 {
		item := overviewResp.Tasks[0].Result[0].Items[0]
		if item.Metrics != nil && item.Metrics.Organic != nil {
			o := item.Metrics.Organic
			artifact.HasRankOverview = true
			artifact.KeywordsCount = o.Count
			artifact.OrganicETV = o.ETV
			artifact.Pos1 = o.Pos1
			artifact.Pos2_3 = o.Pos2_3
			artifact.Pos4_10 = o.Pos4_10
			artifact.Pos11_20 = o.Pos11_20
			artifact.Pos21_30 = o.Pos21_30
			artifact.Pos31Plus = o.Pos31_40 + o.Pos41_50 + o.Pos51_60 + o.Pos61_70 + o.Pos71_80 + o.Pos81_90 + o.Pos91_100
		}
	} else if overviewErr != nil {
		log.Warn("audit authority: domain rank overview failed", "domain", run.TargetDomain, "error", overviewErr)
	}

	// Both sources down = a real collector failure (constraint upstream);
	// one source down degrades inside the artifact.
	if summaryErr != nil && overviewErr != nil {
		return nil, fmt.Errorf("authority collector: backlinks summary (%v) and rank overview (%v) both failed", summaryErr, overviewErr)
	}

	return artifact, nil
}
