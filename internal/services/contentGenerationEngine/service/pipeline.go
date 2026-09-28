package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func buildPipeline(dispatcher pipeline.Dispatcher) *pipeline.Pipeline {
	p, err := pipeline.NewBuilder(dispatcher).
		// Stages
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGESerpFetch, Label: "SERP Fetch"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEUrlScrape, Label: "URL Scrape"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGESerpGapAnalysis, Label: "SERP Gap Analysis"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGETavilySearch, Label: "Tavily Search"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEYouTubeSearch, Label: "YouTube Search"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEYouTubeTranscript, Label: "YouTube Transcript"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEYouTubeSummary, Label: "YouTube Summary"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEOutlineGeneration, Label: "Outline Generation"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEArticleGeneration, Label: "Article Generation"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEInternalLinkInsertion, Label: "Internal Link Insertion"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEImageGeneration, Label: "Image Generation"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEImageReplacement, Label: "Image Replacement"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGESchemaGeneration, Label: "Schema Generation"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEMetaAssetsGeneration, Label: "Meta Assets Generation"}).
		AddStage(pipeline.Stage{ProcessType: cge.ProcessCGEFinalAssembly, Label: "Final Assembly"}).
		// Flow 1: SERP Fetch → URL Scrape (fan-out) → SERP Gap Analysis
		AddEdge(cge.ProcessCGESerpFetch, cge.ProcessCGEUrlScrape, dispatchUrlScrapeFanOut).
		AddEdge(cge.ProcessCGEUrlScrape, cge.ProcessCGESerpGapAnalysis, dispatchCGEStandard).
		// Flow 3: YouTube Search → YouTube Transcript (fan-out) → YouTube Summary
		AddEdge(cge.ProcessCGEYouTubeSearch, cge.ProcessCGEYouTubeTranscript, dispatchYouTubeTranscriptFanOut).
		AddEdge(cge.ProcessCGEYouTubeTranscript, cge.ProcessCGEYouTubeSummary, dispatchCGEStandard).
		// Terminal → Outline (fan-in handled in handlers via AtomicClaimOutlineDispatch)
		AddEdge(cge.ProcessCGESerpGapAnalysis, cge.ProcessCGEOutlineGeneration, dispatchCGEStandard).
		AddEdge(cge.ProcessCGETavilySearch, cge.ProcessCGEOutlineGeneration, dispatchCGEStandard).
		AddEdge(cge.ProcessCGEYouTubeSummary, cge.ProcessCGEOutlineGeneration, dispatchCGEStandard).
		AddEdge(cge.ProcessCGEOutlineGeneration, cge.ProcessCGEArticleGeneration, dispatchCGEStandard).
		// Article → Internal Link Insertion → Image Generation. Internal linking
		// runs as its own step (after the article is written, before images) so
		// the sitemap never biases the article draft.
		AddEdge(cge.ProcessCGEArticleGeneration, cge.ProcessCGEInternalLinkInsertion, dispatchCGEStandard).
		// Internal Link Insertion → Image Generation (fan-out: thumbnail + mid-article)
		AddEdge(cge.ProcessCGEInternalLinkInsertion, cge.ProcessCGEImageGeneration, dispatchImageGenerationFanOut).
		// Image Generation → Image Replacement (fan-in handled in handler via AtomicClaimImageReplacementDispatch)
		AddEdge(cge.ProcessCGEImageGeneration, cge.ProcessCGEImageReplacement, dispatchCGEStandard).
		// Image Replacement → Schema + Meta Assets (parallel)
		AddEdge(cge.ProcessCGEImageReplacement, cge.ProcessCGESchemaGeneration, dispatchCGEStandard).
		AddEdge(cge.ProcessCGEImageReplacement, cge.ProcessCGEMetaAssetsGeneration, dispatchCGEStandard).
		// Schema + Meta Assets → Final Assembly (fan-in handled in handler via AtomicClaimFinalAssemblyDispatch)
		AddEdge(cge.ProcessCGESchemaGeneration, cge.ProcessCGEFinalAssembly, dispatchCGEStandard).
		AddEdge(cge.ProcessCGEMetaAssetsGeneration, cge.ProcessCGEFinalAssembly, dispatchCGEStandard).
		Build()

	if err != nil {
		panic(fmt.Errorf("failed to build CGE pipeline: %w", err))
	}
	return p
}

// dispatchCGEStandard dispatches a CGEStandardPayload using the master context ID
// carried in DispatchContext.WebEntityContextID.
func dispatchCGEStandard(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	return d.Dispatch(ctx, string(next), dc.UserID, cge.CGEStandardPayload{
		WebEntityMasterContextID: dc.WebEntityContextID,
	})
}

// dispatchImageGenerationFanOut dispatches two image generation messages — one for
// thumbnail and one for mid-article — using the same handler with different positions.
func dispatchImageGenerationFanOut(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	positions := []string{models.ImagePositionThumbnail, models.ImagePositionMidArticle}
	for _, pos := range positions {
		payload := cge.CGEImageGenerationPayload{
			WebEntityMasterContextID: dc.WebEntityContextID,
			Position:                 pos,
		}
		if err := d.Dispatch(ctx, string(next), dc.UserID, payload); err != nil {
			return fmt.Errorf("image generation fan-out: dispatch for %s: %w", pos, err)
		}
	}
	return nil
}

// dispatchYouTubeTranscriptFanOut reads YouTubeVideos from the master context and
// dispatches one CGE_YOUTUBE_TRANSCRIPT message per video.
func dispatchYouTubeTranscriptFanOut(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	masterContextID := dc.WebEntityContextID

	_, mc, err := models.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("youtube transcript fan-out: get master context: %w", err)
	}

	for _, video := range mc.YouTubeVideos {
		payload := cge.CGEYouTubeTranscriptPayload{
			WebEntityMasterContextID: masterContextID,
			VideoID:                  video.VideoID,
			Title:                    video.Title,
			Channel:                  video.Channel,
		}
		if err := d.Dispatch(ctx, string(next), dc.UserID, payload); err != nil {
			return fmt.Errorf("youtube transcript fan-out: dispatch for %s: %w", video.VideoID, err)
		}
	}
	return nil
}

// dispatchUrlScrapeFanOut reads the SERP organic URLs from the master context and
// dispatches one CGE_URL_SCRAPE message per URL (0-3). If no URLs were found,
// it marks the downstream steps done and checks the fan-in gate.
func dispatchUrlScrapeFanOut(ctx context.Context, d pipeline.Dispatcher, next pipeline.ProcessType, dc pipeline.DispatchContext) error {
	masterContextID := dc.WebEntityContextID // repurposed to carry master context ID

	_, mc, err := models.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("url scrape fan-out: get master context: %w", err)
	}

	if mc.SerpData == nil || len(mc.SerpData.TopStructures) == 0 {
		// No URLs to scrape — mark URL scrape + gap analysis as done
		if err := models.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
			ProcessMetadata: &models.CGEProcessMetadata{
				ResearchStepStatus: models.CGEResearchStepStatus{
					UrlScrapeDone:       true,
					SerpGapAnalysisDone: true,
				},
			},
		}); err != nil {
			return fmt.Errorf("url scrape fan-out: mark skipped: %w", err)
		}
		return checkFanInFromDispatchFunc(ctx, d, masterContextID, dc.UserID)
	}

	// Dispatch one message per URL
	for _, structure := range mc.SerpData.TopStructures {
		payload := cge.CGEUrlScrapePayload{
			WebEntityMasterContextID: masterContextID,
			URL:                      structure.URL,
		}
		if err := d.Dispatch(ctx, string(next), dc.UserID, payload); err != nil {
			return fmt.Errorf("url scrape fan-out: dispatch for %s: %w", structure.URL, err)
		}
	}
	return nil
}
