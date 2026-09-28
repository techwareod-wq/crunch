package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	ErrWebEntityContextNotFound = errors.New("web entity context not found")
	ErrPipelineNotReady         = errors.New("site intelligence pipeline has not completed clustering")
	ErrClusterNotFound          = errors.New("cluster index out of range")
	// ErrManualKeywordDuplicate is returned by the HTTP stage when the keyword
	// already exists (case-insensitive) within the WebEntityContext.
	ErrManualKeywordDuplicate = errors.New("keyword already exists for this web entity")
	// ErrManualKeywordNoData is returned when DataForSEO has no row matching the
	// user-typed keyword, so there is nothing to enrich.
	ErrManualKeywordNoData = errors.New("no keyword data found for the provided keyword")
)

func (s *seoBlogGeneratorSiteIntelligence) GetKeywordData(ctx context.Context, userId, webEntityId string) (*dto.KeywordDataResponse, error) {
	wec, err := s.getCompletedWebEntityContext(ctx, userId, webEntityId)
	if err != nil {
		return nil, err
	}

	lookups, err := s.loadKeywordLookups(ctx, wec.ID.Hex())
	if err != nil {
		return nil, err
	}

	usage, err := s.buildUsage(ctx, wec)
	if err != nil {
		return nil, err
	}

	return buildKeywordDataResponse(wec, lookups, usage, s.values.SupportingKeywordsPageSize), nil
}

// buildUsage assembles the Usage block: manual-keyword caps plus the
// trial-mode sizing and upgrade state. The article count is the web entity's
// monotonic lifetime counter — the same number the orchestrate cap check
// reads, so "articles left" in the UI always matches the gate. Only computed
// for trial-mode WECs — full mode is uncapped so the zero is meaningful.
func (s *seoBlogGeneratorSiteIntelligence) buildUsage(ctx context.Context, wec *models.WebEntityContext) (dto.Usage, error) {
	usage := dto.Usage{
		ManualUsed:   s.values.Manual.HardcodedUsed,
		ManualLimit:  s.values.Manual.HardcodedLimit,
		SIEMode:      wec.EffectiveSIEMode(),
		UpgradeState: wec.UpgradeState,
	}
	if wec.EffectiveSIEMode() != models.SIEModeTrial {
		return usage, nil
	}
	usage.MaxArticles = s.values.Trial.MaxArticles
	usage.MaxKeywords = s.values.Trial.MaxPersistedKeywords
	usage.ClusterCount = s.values.Trial.ClusterCount

	used, err := models.GetLifetimeArticlesGeneratedForEntity(ctx, wec.WebEntityID)
	if err != nil {
		return usage, err
	}
	usage.ArticlesUsed = used
	return usage, nil
}

// GetClusterSupportingKeywords returns one zero-based page of a single cluster's
// supporting keywords. The cluster is addressed by its index in wec.Clusters
// because cluster IDs come from the LLM and are not guaranteed unique.
func (s *seoBlogGeneratorSiteIntelligence) GetClusterSupportingKeywords(ctx context.Context, userId, webEntityId string, clusterIndex, page int) (*dto.ClusterSupportingResponse, error) {
	wec, err := s.getCompletedWebEntityContext(ctx, userId, webEntityId)
	if err != nil {
		return nil, err
	}

	if clusterIndex < 0 || clusterIndex >= len(wec.Clusters) {
		return nil, ErrClusterNotFound
	}
	if page < 0 {
		page = 0
	}

	lookups, err := s.loadKeywordLookups(ctx, wec.ID.Hex())
	if err != nil {
		return nil, err
	}

	supportingIDs := wec.Clusters[clusterIndex].SupportingKeywordIDs
	total := len(supportingIDs)
	pageSize := s.values.SupportingKeywordsPageSize

	start := page * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	supporting := make([]dto.KeywordDTO, 0, end-start)
	for _, kwID := range supportingIDs[start:end] {
		kw, ok := lookups.keywordByID[kwID]
		if !ok {
			continue
		}
		supporting = append(supporting, toKeywordDTO(kw, lookups.articleByKeyword[kwID], lookups.articleCountByKeyword[kwID], lookups.erroredArticles))
	}

	return &dto.ClusterSupportingResponse{
		ClusterIndex:    clusterIndex,
		Page:            page,
		PageSize:        pageSize,
		SupportingTotal: total,
		Supporting:      supporting,
	}, nil
}

func (s *seoBlogGeneratorSiteIntelligence) getCompletedWebEntityContext(ctx context.Context, userId, webEntityId string) (*models.WebEntityContext, error) {
	found, wec, err := models.GetWebEntityContextFromWebEntityAndUserID(ctx, webEntityId, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !found {
		return nil, ErrWebEntityContextNotFound
	}
	// Keyword data exists once clustering has finished. Status may already be
	// SIEStatusSchedulingDone (9) after the scheduling engine runs; do not
	// require exact equality with SIEStatusClusteringDone (8). Use EffectiveStatus
	// so a run that finished clustering and then errored downstream (Status ==
	// SIEStatusError) still surfaces its keyword data.
	if wec.EffectiveStatus() < models.SIEStatusClusteringDone {
		return nil, ErrPipelineNotReady
	}
	return wec, nil
}

// keywordLookups bundles the per-WEC maps the read path needs to project
// Keyword documents into wire DTOs: the keyword-by-ID index, the latest
// scheduled article per keyword, and the set of scheduled articles whose CGE
// run errored. Shared by the full keyword-data payload and the paginated
// per-cluster supporting endpoint.
type keywordLookups struct {
	keywords         []models.Keyword
	keywordByID      map[primitive.ObjectID]models.Keyword
	articleByKeyword map[primitive.ObjectID]*models.ScheduledArticle
	// articleCountByKeyword counts every article per keyword, not just the
	// latest one articleByKeyword keeps — keyword reuse makes >1 a normal state
	// and the DTO surfaces the count next to the used flag.
	articleCountByKeyword map[primitive.ObjectID]int
	erroredArticles       map[string]struct{}
}

func (s *seoBlogGeneratorSiteIntelligence) loadKeywordLookups(ctx context.Context, wecID string) (*keywordLookups, error) {
	keywords, err := models.GetKeywordsForWEC(ctx, wecID)
	if err != nil {
		return nil, fmt.Errorf("load keywords for WEC %s: %w", wecID, err)
	}

	// Article lifecycle (scheduled/generating/readyForReview/draft/published)
	// lives on ScheduledArticle, not on Keyword. Hydrate by keyword ID so the
	// keyword cards can surface the supporting-article status.
	scheduledArticles, err := models.GetScheduledArticlesByWebEntityContext(ctx, wecID)
	if err != nil {
		return nil, fmt.Errorf("load scheduled articles for WEC %s: %w", wecID, err)
	}

	// Hydrate master contexts so the keyword DTO can surface the wire-only
	// "error" status when the linked CGE run failed — mirrors the override
	// applied by the scheduled-articles read path.
	saIDs := make([]string, 0, len(scheduledArticles))
	for _, sa := range scheduledArticles {
		saIDs = append(saIDs, sa.ID.Hex())
	}
	mcs, err := models.GetMasterContextsByScheduledArticleIDs(ctx, saIDs)
	if err != nil {
		return nil, fmt.Errorf("load master contexts for WEC %s: %w", wecID, err)
	}

	keywordByID := make(map[primitive.ObjectID]models.Keyword, len(keywords))
	for _, kw := range keywords {
		keywordByID[kw.ID] = kw
	}

	// A keyword may carry several articles (manual reuse). The latest
	// schedule_date wins so the surfaced status reflects the most recent slot;
	// the count map keeps the full per-keyword total for the used badge.
	articleByKeyword := make(map[primitive.ObjectID]*models.ScheduledArticle, len(scheduledArticles))
	articleCountByKeyword := make(map[primitive.ObjectID]int, len(scheduledArticles))
	for _, sa := range scheduledArticles {
		articleCountByKeyword[sa.KeywordID]++
		if existing, ok := articleByKeyword[sa.KeywordID]; ok && !sa.ScheduleDate.After(existing.ScheduleDate) {
			continue
		}
		articleByKeyword[sa.KeywordID] = sa
	}

	erroredArticles := make(map[string]struct{}, len(mcs))
	for i := range mcs {
		if mcs[i].Status == models.CGEStatusError {
			erroredArticles[mcs[i].ScheduledArticleID.Hex()] = struct{}{}
		}
	}

	return &keywordLookups{
		keywords:              keywords,
		keywordByID:           keywordByID,
		articleByKeyword:      articleByKeyword,
		articleCountByKeyword: articleCountByKeyword,
		erroredArticles:       erroredArticles,
	}, nil
}

// buildKeywordDataResponse assembles the keyword-data payload. pageSize
// (values.siteIntelligence.supportingKeywordsPageSize) caps the supporting
// keywords embedded per cluster; usage and pageSize are passed in rather than
// read from the service because this is a package-level function.
func buildKeywordDataResponse(wec *models.WebEntityContext, lookups *keywordLookups, usage dto.Usage, pageSize int) *dto.KeywordDataResponse {
	clusters := make([]dto.ClusterDTO, 0, len(wec.Clusters))
	for _, c := range wec.Clusters {
		pillar := lookups.keywordByID[c.PillarKeywordID]

		// Embed only the first page of supporting keywords; the client fetches
		// the rest on demand. SupportingTotal counts every resolvable ID so the
		// client can compute the page count.
		supportingTotal := 0
		supporting := make([]dto.KeywordDTO, 0, pageSize)
		for _, kwID := range c.SupportingKeywordIDs {
			kw, ok := lookups.keywordByID[kwID]
			if !ok {
				continue
			}
			supportingTotal++
			if len(supporting) < pageSize {
				supporting = append(supporting, toKeywordDTO(kw, lookups.articleByKeyword[kwID], lookups.articleCountByKeyword[kwID], lookups.erroredArticles))
			}
		}

		clusters = append(clusters, dto.ClusterDTO{
			ID:              c.ClusterID,
			Name:            c.ClusterName,
			KeywordCount:    1 + supportingTotal,
			PillarPublished: c.PillarPublished,
			Pillar:          toKeywordDTO(pillar, lookups.articleByKeyword[c.PillarKeywordID], lookups.articleCountByKeyword[c.PillarKeywordID], lookups.erroredArticles),
			Supporting:      supporting,
			SupportingTotal: supportingTotal,
		})
	}

	return &dto.KeywordDataResponse{
		Usage:         usage,
		TotalKeywords: len(lookups.keywords),
		TotalClusters: len(wec.Clusters),
		Clusters:      clusters,
	}
}

// toKeywordDTO projects a keyword into its wire shape. sa is the latest
// article booked on the keyword (nil when unused); articleCount is the full
// per-keyword article total from articleCountByKeyword.
func toKeywordDTO(k models.Keyword, sa *models.ScheduledArticle, articleCount int, erroredScheduledArticleIDs map[string]struct{}) dto.KeywordDTO {
	// Status defaults to "queued" (no slot booked yet). When a ScheduledArticle
	// exists, its lifecycle status is the source of truth — Keyword.Status is
	// only used internally (e.g. "refresh_candidate" by post-processing). With
	// keyword reuse, status reflects only the latest article; used/articleCount
	// carry the aggregate.
	status := dto.KeywordStatusQueued
	var scheduledFor *string
	if sa != nil {
		if articleCount == 0 {
			articleCount = 1
		}
		status = sa.Status.String()
		// Override with the wire-only "error" label when the linked CGE run
		// failed — matches the scheduled-articles read path so /keywords
		// surfaces the same lifecycle the dashboard does.
		if _, errored := erroredScheduledArticleIDs[sa.ID.Hex()]; errored {
			status = dto.KeywordStatusError
		}
		s := sa.ScheduleDate.Format("Jan 2")
		scheduledFor = &s
	}

	return dto.KeywordDTO{
		ID:            k.ID.Hex(),
		Keyword:       k.Keyword,
		Funnel:        string(k.Funnel),
		Volume:        k.Volume,
		Difficulty:    k.KeywordDifficulty,
		CPC:           k.CPC,
		Score:         k.OpportunityScore,
		Status:        status,
		ScheduledFor:  scheduledFor,
		Used:          articleCount > 0,
		ArticleCount:  articleCount,
		ManuallyAdded: k.ManuallyAdded,
		Completed:     k.Completed,
	}
}
