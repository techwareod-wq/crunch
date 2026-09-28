package service

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"github.com/atharva-ng/crunch/internal/util/log"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Orchestrate finds-or-creates the WebEntityMasterContext and dispatches only incomplete steps.
// Gated on the parent WebEntityContext having reached SIEStatusSchedulingDone (9) —
// the article calendar must exist before we start spending tokens on per-article research.
func (s *contentGenerationEngineService) Orchestrate(ctx context.Context, userId string, payload cge.CGEOrchestratePayload) error {
	ok, wec, err := s.store.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", payload.WebEntityContextID)
	}
	if wec.Status < models.SIEStatusSchedulingDone {
		return fmt.Errorf("content generation requires scheduling done (status %d), got %d", models.SIEStatusSchedulingDone, wec.Status)
	}

	// Flip the calendar slot to generating before any expensive work runs so
	// the UI reflects the in-flight state. The seoblog manual trigger may not
	// have a scheduled article attached, so this is best-effort.
	if payload.ScheduledArticleID != "" {
		if err := s.store.SetScheduledArticleStatus(ctx, payload.ScheduledArticleID, models.ScheduledArticleStatusGenerating); err != nil {
			return fmt.Errorf("mark scheduled article generating: %w", err)
		}
		// Charge the slot against its web entity's lifetime generation counter
		// (first start only — retries lose the claim). Accounting failure must
		// not block generation: undercounting favors the user.
		if err := s.store.RecordArticleGenerationStart(ctx, payload.ScheduledArticleID); err != nil {
			log.Error("record article generation start failed", "error", err,
				"scheduled_article_id", payload.ScheduledArticleID, "user_id", userId)
		}
	}

	keywordID, err := primitive.ObjectIDFromHex(payload.KeywordID)
	if err != nil {
		return fmt.Errorf("invalid keyword id: %w", err)
	}

	ok, kw, err := s.store.GetKeyword(ctx, payload.KeywordID)
	if err != nil {
		return fmt.Errorf("get keyword: %w", err)
	}
	if !ok {
		return fmt.Errorf("keyword not found: %s", payload.KeywordID)
	}

	// One master context per calendar slot, so the find-or-create must key on
	// the slot id. Keyword reuse means several slots can share a keyword — a
	// keyword-scoped lookup here would resurface ANOTHER slot's finished
	// pipeline, hit the "already complete" no-op below, and strand the new
	// slot in `generating` forever. The keyword+WEC lookup survives only as a
	// fallback for slot-less payloads (every current dispatcher sets the slot).
	var isExisting bool
	var mc *models.WebEntityMasterContext
	if payload.ScheduledArticleID != "" {
		isExisting, mc, err = s.store.GetWebEntityMasterContextByScheduledArticleID(ctx, payload.ScheduledArticleID)
	} else {
		isExisting, mc, err = s.store.GetWebEntityMasterContextByKeywordAndWEC(ctx, payload.WebEntityContextID, keywordID)
	}
	if err != nil {
		return fmt.Errorf("find master context: %w", err)
	}

	var masterContextID string

	if isExisting {
		masterContextID = mc.ID.Hex()

		// Pipeline already complete — nothing to do
		if mc.Status == models.CGEStatusDone || mc.Status == models.CGEStatusReadyForReview {
			return nil
		}

		// Only reset on error — in-progress states (Processing, OutlineGenerated)
		// are actively running and just need incomplete steps dispatched
		if mc.Status == models.CGEStatusError {
			if err := s.store.ResetCGEForRetry(ctx, masterContextID, mc); err != nil {
				return fmt.Errorf("reset for retry: %w", err)
			}
		} else {
			// Always clear error data on re-trigger
			if err := s.store.ClearCGEErrorData(ctx, masterContextID); err != nil {
				return fmt.Errorf("clear error data: %w", err)
			}
		}
	} else {
		mc, masterContextID, err = s.createMasterContext(ctx, userId, payload, kw)
		if err != nil {
			return err
		}
	}

	research := mc.ProcessMetadata.ResearchStepStatus

	if research.AllResearchDone() {
		return s.dispatchIncompletePostResearchSteps(ctx, userId, mc, masterContextID)
	}

	return s.dispatchIncompleteResearchSteps(ctx, userId, kw, research, masterContextID)
}

// createMasterContext resolves parent entities and creates a new WebEntityMasterContext.
// SecondaryKeywords are hydrated from the keyword collection via the cluster
// referenced on the triggering keyword doc.
func (s *contentGenerationEngineService) createMasterContext(ctx context.Context, userId string, payload cge.CGEOrchestratePayload, kw *models.Keyword) (*models.WebEntityMasterContext, string, error) {
	ok, wec, err := s.store.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return nil, "", fmt.Errorf("get web entity context: %w", err)
	}
	if !ok {
		return nil, "", fmt.Errorf("web entity context not found: %s", payload.WebEntityContextID)
	}

	ok, we, err := s.store.GetWebEntityByID(ctx, wec.WebEntityID.Hex())
	if err != nil {
		return nil, "", fmt.Errorf("get web entity: %w", err)
	}
	if !ok {
		return nil, "", fmt.Errorf("web entity not found: %s", wec.WebEntityID.Hex())
	}

	secondaryKeywords, err := s.resolveSecondaryKeywords(ctx, wec, kw)
	if err != nil {
		return nil, "", err
	}

	var userDR int
	if we.BusinessContext != nil {
		userDR = we.BusinessContext.UserDomainRating
	}

	// Resolve the effective internal-linking toggle: the triggering article's
	// per-article override wins, otherwise inherit the web entity default
	// (unset = enabled).
	internalLinkingEnabled := we.InternalLinkingEnabledOrDefault()
	if payload.InternalLinkingEnabled != nil {
		internalLinkingEnabled = *payload.InternalLinkingEnabled
	}

	// Resolve the effective image styling once and stamp it as plain strings
	// so the image-generation step reads it here without touching the
	// ScheduledArticle / WebEntity.
	thumbnailStyle, thumbnailStylePrompt, midArticleStylePrompt := resolveImageStyle(we, payload.ThumbnailStyle)

	// Snapshot the learned text artifacts (master-context semantics — see the
	// field docs).
	style := we.StyleProfile()

	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return nil, "", fmt.Errorf("invalid user ID: %w", err)
	}
	wecOID, err := primitive.ObjectIDFromHex(payload.WebEntityContextID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid web entity context ID: %w", err)
	}
	// ScheduledArticleID is optional (empty on the manual-trigger path) — leave
	// it as the zero ObjectID when absent rather than failing the parse.
	var scheduledArticleOID primitive.ObjectID
	if payload.ScheduledArticleID != "" {
		scheduledArticleOID, err = primitive.ObjectIDFromHex(payload.ScheduledArticleID)
		if err != nil {
			return nil, "", fmt.Errorf("invalid scheduled article ID: %w", err)
		}
	}

	mc := &models.WebEntityMasterContext{
		UserID:                 userOID,
		WebEntityContextID:     wecOID,
		ScheduledArticleID:     scheduledArticleOID,
		KeywordID:              kw.ID,
		ArticleType:            payload.ArticleType,
		ProposedTitle:          payload.ProposedTitle,
		AdditionalInstructions: payload.AdditionalInstructions,
		ScheduledDate:          payload.ScheduledDate,
		Sources:                payload.Sources,
		UserRankingPosition:    payload.UserRankingPosition,
		UserRankingURL:         payload.UserRankingURL,
		CompetitorRankings:     payload.CompetitorRankings,
		WebsiteURL:             we.WebsiteUrl,
		BusinessContext:        we.BusinessContext,
		Competitors:            we.Competitors,
		CountryCode:            we.CountryCode,
		LocationCode:           we.LocationCode,
		UserDR:                 userDR,
		SecondaryKeywords:      secondaryKeywords,
		InternalLinkingEnabled: internalLinkingEnabled,
		ThumbnailStyle:         thumbnailStyle,
		ToneProfile:            style.Tone(),
		StructurePattern:       style.Structure(),
		ThumbnailStylePrompt:   thumbnailStylePrompt,
		MidArticleStylePrompt:  midArticleStylePrompt,
		Status:                 models.CGEStatusProcessing,
	}

	if err := s.store.CreateWebEntityMasterContext(ctx, mc); err != nil {
		return nil, "", fmt.Errorf("create master context: %w", err)
	}

	return mc, mc.ID.Hex(), nil
}

// resolveImageStyle picks the effective image styling for a run. The two
// positions resolve independently:
//   - THUMBNAIL ladder: per-article explicit catalog pick → learned thumbnail
//     style → entity thumbnail_style → system default. When the learned style
//     wins, thumbnailStyle stamps empty so the template lookup routes to the
//     custom learned template; an explicit pick wins over it (the learned
//     prompt still stamps for observability but the catalog template renders).
//   - MID-ARTICLE: learned mid-article style when present (there is no
//     per-article picker for the slot), else the static default template.
func resolveImageStyle(we *models.WebEntity, override *string) (thumbnailStyle, thumbnailStylePrompt, midArticleStylePrompt string) {
	profile := we.StyleProfile()
	thumbLearned := profile.ThumbnailImageStyle()
	midLearned := profile.MidArticleImageStyle()

	if override != nil && prompts.IsValidThumbnailStyle(*override) {
		return *override, thumbLearned, midLearned
	}
	if thumbLearned != "" {
		return "", thumbLearned, midLearned
	}
	return we.ThumbnailStyleOrDefault(), "", midLearned
}

// resolveSecondaryKeywords looks up the cluster the triggering keyword belongs
// to, then hydrates the supporting keyword docs so the master context can
// store their text. Returns nil when the keyword has no cluster — the legacy
// behavior treated this case the same way.
func (s *contentGenerationEngineService) resolveSecondaryKeywords(ctx context.Context, wec *models.WebEntityContext, kw *models.Keyword) ([]string, error) {
	if kw.Cluster == "" {
		return nil, nil
	}
	var supportingIDs []primitive.ObjectID
	for _, cluster := range wec.Clusters {
		if cluster.ClusterID == kw.Cluster {
			supportingIDs = cluster.SupportingKeywordIDs
			break
		}
	}
	if len(supportingIDs) == 0 {
		return nil, nil
	}
	supporting, err := s.store.GetKeywordsByIDs(ctx, supportingIDs)
	if err != nil {
		return nil, fmt.Errorf("hydrate supporting keywords: %w", err)
	}
	out := make([]string, 0, len(supporting))
	for _, kw := range supporting {
		out = append(out, kw.Keyword)
	}
	return out, nil
}

// dispatchIncompleteResearchSteps dispatches only the research steps that haven't completed.
// Respects dependency chains: SerpFetch → SerpGapAnalysis, YouTubeSearch → YouTubeSummary.
func (s *contentGenerationEngineService) dispatchIncompleteResearchSteps(
	ctx context.Context, userId string, kw *models.Keyword,
	research models.CGEResearchStepStatus, masterContextID string,
) error {
	sp := cge.CGEStandardPayload{WebEntityMasterContextID: masterContextID}

	// SERP flow: SerpFetch → UrlScrape → SerpGapAnalysis
	if !research.SerpFetchDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGESerpFetch), userId, sp); err != nil {
			return fmt.Errorf("dispatch SERP fetch: %w", err)
		}
	} else if !research.SerpGapAnalysisDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGESerpGapAnalysis), userId, sp); err != nil {
			return fmt.Errorf("dispatch SERP gap analysis: %w", err)
		}
	}

	// Tavily flow — each variant is independent
	year := time.Now().Year()
	if !research.TavilyNewsDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGETavilySearch), userId, cge.CGETavilySearchPayload{
			WebEntityMasterContextID: masterContextID,
			Variant:                  "news",
			Query:                    fmt.Sprintf("%s news update %d", kw.Keyword, year),
		}); err != nil {
			return fmt.Errorf("dispatch tavily news: %w", err)
		}
	}
	if !research.TavilyExpertDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGETavilySearch), userId, cge.CGETavilySearchPayload{
			WebEntityMasterContextID: masterContextID,
			Variant:                  "expert",
			Query:                    fmt.Sprintf("%s expert opinion best practice", kw.Keyword),
		}); err != nil {
			return fmt.Errorf("dispatch tavily expert: %w", err)
		}
	}
	if !research.TavilyMistakesDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGETavilySearch), userId, cge.CGETavilySearchPayload{
			WebEntityMasterContextID: masterContextID,
			Variant:                  "mistakes",
			Query:                    fmt.Sprintf("%s common mistakes problems issues", kw.Keyword),
		}); err != nil {
			return fmt.Errorf("dispatch tavily mistakes: %w", err)
		}
	}

	// YouTube flow: YouTubeSearch → YouTubeTranscript → YouTubeSummary
	if !research.YouTubeSearchDone {
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEYouTubeSearch), userId, sp); err != nil {
			return fmt.Errorf("dispatch YouTube search: %w", err)
		}
	} else if !research.YouTubeSummaryDone {
		// YouTube search done but summary not done — dispatch summary directly
		if err := s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEYouTubeSummary), userId, sp); err != nil {
			return fmt.Errorf("dispatch YouTube summary: %w", err)
		}
	}

	return nil
}

// dispatchIncompletePostResearchSteps checks the pipeline status right-to-left
// and dispatches from the furthest incomplete point using pipeline.DispatchNext.
// Pipeline: Outline → Article → ImageGen → ImageReplacement → Schema+Meta → FinalAssembly
func (s *contentGenerationEngineService) dispatchIncompletePostResearchSteps(
	ctx context.Context, userId string, mc *models.WebEntityMasterContext, masterContextID string,
) error {
	dc := pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID,
	}
	pa := mc.ProcessMetadata.PostArticleStatus

	// Walk right-to-left: check the furthest stage first
	if pa.AllMetaDone() {
		return s.pipeline.DispatchNext(ctx, cge.ProcessCGESchemaGeneration, dc)
	}

	if pa.ImageReplacementDone {
		// DispatchNext from ImageReplacement dispatches both Schema and Meta.
		// But if one is already done, dispatch only the missing one directly.
		if !pa.SchemaGenerationDone && !pa.MetaAssetsGenerationDone {
			return s.pipeline.DispatchNext(ctx, cge.ProcessCGEImageReplacement, dc)
		}
		if !pa.SchemaGenerationDone {
			return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGESchemaGeneration), userId,
				cge.CGEStandardPayload{WebEntityMasterContextID: masterContextID})
		}
		return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEMetaAssetsGeneration), userId,
			cge.CGEStandardPayload{WebEntityMasterContextID: masterContextID})
	}

	if pa.AllImageGenDone() {
		return s.pipeline.DispatchNext(ctx, cge.ProcessCGEImageGeneration, dc)
	}

	if mc.ArticleContent != "" {
		// Internal link insertion sits between article generation and image
		// generation. If it hasn't completed, (re-)run it directly — its done
		// flag gates re-runs so links aren't double-inserted. On completion it
		// dispatches the image-generation fan-out itself.
		if !pa.InternalLinkInsertionDone {
			return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEInternalLinkInsertion), userId,
				cge.CGEStandardPayload{WebEntityMasterContextID: masterContextID})
		}
		if !pa.ImageGenThumbnailDone && !pa.ImageGenMidArticleDone {
			// Both missing — fan out to image generation from the link step.
			return s.pipeline.DispatchNext(ctx, cge.ProcessCGEInternalLinkInsertion, dc)
		}
		position := "thumbnail"
		if !pa.ImageGenMidArticleDone {
			position = "mid-article"
		}
		return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEImageGeneration), userId,
			cge.CGEImageGenerationPayload{WebEntityMasterContextID: masterContextID, Position: position})
	}

	if mc.Outline != nil {
		return s.pipeline.DispatchNext(ctx, cge.ProcessCGEOutlineGeneration, dc)
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEOutlineGeneration), userId,
		cge.CGEStandardPayload{WebEntityMasterContextID: masterContextID})
}
