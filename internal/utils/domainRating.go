package utils

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// FetchDomainRating returns the domain's backlink authority score (result.rank,
// on the 0–100 scale) for use as the user's domain rating. A zero/absent rank
// means the API had no authority data for the domain; the caller decides
// whether to treat that as "unknown" (skip persistence) or fall back to a
// default.
//
// The backlinks-task shape here (rank_scale one_hundred, dofollow-only filter,
// include_subdomains) is the single source of truth for domain-rating lookups —
// both onboarding (at competitor discovery) and SIE (its GetUserDomainRating
// fallback step) call through here so the semantics never drift between them.
func FetchDomainRating(ctx context.Context, dfs interfaces.DataForSEO, rawURL string) (int, error) {
	task := dto.BacklinksSummaryTask{
		Target:              ExtractDomain(rawURL),
		InternalListLimit:   10,
		IncludeSubdomains:   true,
		BacklinksFilters:    []interface{}{"dofollow", "=", true},
		BacklinksStatusType: "all",
		RankScale:           "one_hundred",
	}
	resp, err := dfs.GetBacklinksSummary(ctx, dto.BacklinksSummaryRequest{Tasks: []dto.BacklinksSummaryTask{task}})
	if err != nil {
		return 0, fmt.Errorf("failed to get backlinks summary for %s: %w", task.Target, err)
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		return 0, fmt.Errorf("no backlinks summary found for %s", task.Target)
	}
	return resp.Tasks[0].Result[0].Rank, nil
}
