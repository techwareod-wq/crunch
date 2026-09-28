package collectors

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// serpCollector builds the SXO artifact (decision 11 — zero new provider
// methods): ranked keywords for the domain via GetKeywordData map deep-pass
// pages to their best keyword; one GetAdvancedSerpResults call per selected
// page (capped at audit.sampling.sxoPages) records the SERP item types the
// page competes against. A domain with zero ranked keywords produces an
// empty artifact — that's data, not a failure. Non-critical.
type serpCollector struct{}

func (serpCollector) ID() core.CollectorID            { return CollectorSERP }
func (serpCollector) Produces() []core.Kind           { return []core.Kind{artifacts.KindSERP} }
func (serpCollector) AppliesTo(*models.AuditRun) bool { return true }
func (serpCollector) Critical() bool                  { return false }

// serpRankedKeywordsLimit bounds the ranked-keywords pull the page→keyword
// map is built from.
const serpRankedKeywordsLimit = 200

// serpDepth is the SERP page-1 depth per keyword.
const serpDepth = 10

// sxoTopResultsCap / sxoPAACap bound the per-keyword competitive snapshot
// persisted for the consensus and question analyses.
const (
	sxoTopResultsCap = 10
	sxoPAACap        = 8
)

func (serpCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	crawl, err := loadCrawlArtifact(ctx, run)
	if err != nil {
		return fmt.Errorf("serp collector: %w", err)
	}

	sxoCap := deps.Values.Sampling.SXOPages
	if sxoCap <= 0 || sxoCap > len(crawl.DeepPassSample) {
		sxoCap = len(crawl.DeepPassSample)
	}
	if sxoCap == 0 {
		return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindSERP, &artifacts.SERPArtifact{})
	}

	locationCode := RunLocationCode(run)
	ranked, err := deps.DFS.GetKeywordData(ctx, dto.GetKeywordDataRequest{Tasks: []dto.GetKeywordDataTask{{
		Target:       run.TargetDomain,
		LanguageName: auditLanguageName,
		LocationCode: locationCode,
		Limit:        serpRankedKeywordsLimit,
		ItemTypes:    []string{"organic"},
	}}})
	if err != nil {
		return fmt.Errorf("serp collector: ranked keywords: %w", err)
	}

	// Best (highest-volume) ranking keyword per URL.
	type pageKeyword struct {
		keyword  string
		volume   int
		position int
	}
	best := map[string]pageKeyword{}
	if len(ranked.Tasks) > 0 && len(ranked.Tasks[0].Result) > 0 {
		for _, item := range ranked.Tasks[0].Result[0].Items {
			u := item.RankedSerpElement.SerpItem.URL
			if u == "" {
				continue
			}
			key := normalizeForMatch(u)
			existing, ok := best[key]
			vol := item.KeywordData.KeywordInfo.SearchVolume
			if !ok || vol > existing.volume {
				best[key] = pageKeyword{
					keyword:  item.KeywordData.Keyword,
					volume:   vol,
					position: item.RankedSerpElement.SerpItem.RankAbsolute,
				}
			}
		}
	}

	artifact := &artifacts.SERPArtifact{}
	for _, pageURL := range crawl.DeepPassSample {
		if len(artifact.Pages) >= sxoCap {
			break
		}
		pk, ok := best[normalizeForMatch(pageURL)]
		if !ok || pk.keyword == "" {
			continue
		}
		serpResp, err := deps.DFS.GetAdvancedSerpResults(ctx, dto.GetAdvancedSerpRequest{Tasks: []dto.AdvancedSerpTask{{
			Keyword:      pk.keyword,
			LocationCode: locationCode,
			LanguageCode: "en",
			Depth:        serpDepth,
		}}})
		if err != nil {
			log.Warn("audit serp: advanced serp fetch failed", "keyword", pk.keyword, "error", err)
			continue
		}
		itemTypes := map[string]bool{}
		var topResults []artifacts.SXOResult
		var paaQuestions []string
		if len(serpResp.Tasks) > 0 && len(serpResp.Tasks[0].Result) > 0 {
			for _, item := range serpResp.Tasks[0].Result[0].Items {
				itemTypes[item.Type] = true
				switch item.Type {
				case "organic":
					if len(topResults) < sxoTopResultsCap && item.Domain != "" {
						topResults = append(topResults, artifacts.SXOResult{
							Rank:   len(topResults) + 1,
							Domain: item.Domain,
							Title:  item.Title,
							URL:    item.URL,
						})
					}
				case "people_also_ask":
					for _, paa := range item.PAAItems() {
						if paa.Title != "" && len(paaQuestions) < sxoPAACap {
							paaQuestions = append(paaQuestions, paa.Title)
						}
					}
				}
			}
		}
		var types []string
		for t := range itemTypes {
			types = append(types, t)
		}
		sortStrings(types)
		artifact.Pages = append(artifact.Pages, artifacts.SXOPage{
			URL:          pageURL,
			Keyword:      pk.keyword,
			SearchVolume: pk.volume,
			Position:     pk.position,
			ItemTypes:    types,
			TopResults:   topResults,
			PAAQuestions: paaQuestions,
		})
	}

	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindSERP, artifact)
}

func normalizeForMatch(raw string) string {
	s := strings.TrimPrefix(raw, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")
	return strings.TrimRight(strings.ToLower(s), "/")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
