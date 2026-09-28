package contentGenerationEngine

import (
	"context"
	"errors"

	"github.com/atharva-ng/crunch/internal/pipeline"
)

// Regeneration errors. The regenerate entry points are user-facing and
// synchronous, so unlike the queue handlers they surface typed errors the
// HTTP layer can map.
var (
	// ErrRegenArticleNotFound covers both a missing scheduled article and one
	// owned by another user — same not-found shape, no existence leak.
	ErrRegenArticleNotFound = errors.New("scheduled article not found")
	// ErrRegenNotGenerated is returned when the pipeline hasn't produced any
	// article content to regenerate against yet.
	ErrRegenNotGenerated = errors.New("article has not been generated yet")
	// ErrRegenInvalidInput is returned for out-of-cap or malformed inputs
	// (empty/oversized selection, oversized instructions, unknown position).
	ErrRegenInvalidInput = errors.New("invalid regeneration input")
	// ErrRegenUnsafeOutput is returned when the LLM output failed safety
	// validation on every attempt (raw HTML, foreign image hosts, structure
	// tampering) — nothing unvalidated is ever returned to the client.
	ErrRegenUnsafeOutput = errors.New("regenerated content failed validation")
)

// RegenerateSectionRequest is the input for a user-triggered section rewrite.
type RegenerateSectionRequest struct {
	ScheduledArticleID string
	// SelectedMarkdown is the passage the user highlighted, exactly as the
	// editor serialises it. Treated strictly as content by the prompt —
	// directives embedded inside it are not followed.
	SelectedMarkdown string
	// Instructions is the optional "what should change" the user typed. This
	// is the one honoured command channel for the rewrite.
	Instructions string
	// Label is the display name the client shows for this rewrite ("Selection"
	// or the section heading). Stored on the pending entry, never prompted.
	Label string
}

// RegeneratedSection is a validated rewrite plus the id of its pending entry
// (recorded on the master context so the regen survives reloads and other
// devices until the user saves or discards it).
type RegeneratedSection struct {
	ID       int64
	Markdown string
}

// RegenerateImageRequest is the input for a user-triggered single-image regen.
type RegenerateImageRequest struct {
	ScheduledArticleID string
	Position           string // models.ImagePositionThumbnail | models.ImagePositionMidArticle
	// Instructions is optional art direction folded into the image prompt as a
	// constrained style-notes block.
	Instructions string
}

// RegeneratedImage points at the freshly generated object. The key is NOT
// persisted anywhere server-side — the client previews the URL (keeping the
// old image for undo) and commits the key via save-draft images[].newS3Key,
// exactly like a user-uploaded replacement.
type RegeneratedImage struct {
	S3Key string
	URL   string
	Alt   string
}

const (
	ProcessCGEOrchestrate           pipeline.ProcessType = "CGE_ORCHESTRATE"
	ProcessCGESerpFetch             pipeline.ProcessType = "CGE_SERP_FETCH"
	ProcessCGEUrlScrape             pipeline.ProcessType = "CGE_URL_SCRAPE"
	ProcessCGESerpGapAnalysis       pipeline.ProcessType = "CGE_SERP_GAP_ANALYSIS"
	ProcessCGETavilySearch          pipeline.ProcessType = "CGE_TAVILY_SEARCH"
	ProcessCGEYouTubeSearch         pipeline.ProcessType = "CGE_YOUTUBE_SEARCH"
	ProcessCGEYouTubeTranscript     pipeline.ProcessType = "CGE_YOUTUBE_TRANSCRIPT"
	ProcessCGEYouTubeSummary        pipeline.ProcessType = "CGE_YOUTUBE_SUMMARY"
	ProcessCGEOutlineGeneration     pipeline.ProcessType = "CGE_OUTLINE_GENERATION"
	ProcessCGEArticleGeneration     pipeline.ProcessType = "CGE_ARTICLE_GENERATION"
	ProcessCGEInternalLinkInsertion pipeline.ProcessType = "CGE_INTERNAL_LINK_INSERTION"
	ProcessCGEImageGeneration       pipeline.ProcessType = "CGE_IMAGE_GENERATION"
	ProcessCGEImageReplacement      pipeline.ProcessType = "CGE_IMAGE_REPLACEMENT"
	ProcessCGESchemaGeneration      pipeline.ProcessType = "CGE_SCHEMA_GENERATION"
	ProcessCGEMetaAssetsGeneration  pipeline.ProcessType = "CGE_META_ASSETS_GENERATION"
	ProcessCGEFinalAssembly         pipeline.ProcessType = "CGE_FINAL_ASSEMBLY"
)

// CGEMetadata carries identity fields for standard CGE sub-process handlers.
type CGEMetadata struct {
	WebEntityID              string
	WebEntityContextID       string
	WebEntityMasterContextID string
}

// CGEOrchestratePayload is the payload for the orchestrate entry point.
// KeywordID references a doc in the `keyword` collection — all triggering
// keyword data (volume, kd, cpc, intent, funnel, cluster, opportunity score)
// is read from that doc. ScheduledArticleID ties this run back to the
// calendar slot SE produced.
type CGEOrchestratePayload struct {
	ScheduledArticleID string `json:"scheduledArticleId"`
	WebEntityContextID string `json:"webEntityContextId"`
	KeywordID          string `json:"keywordId"`
	ArticleType        string `json:"articleType"`
	ProposedTitle      string `json:"proposedTitle"`
	// AdditionalInstructions is the user-supplied dashboard guidance copied
	// onto the WebEntityMasterContext at create time. Empty when the user
	// didn't set anything on the calendar slot.
	AdditionalInstructions string         `json:"additionalInstructions"`
	ScheduledDate          string         `json:"scheduledDate"`
	Sources                []string       `json:"sources"`
	UserRankingPosition    int            `json:"userRankingPosition"`
	UserRankingURL         string         `json:"userRankingUrl"`
	CompetitorRankings     map[string]int `json:"competitorRankings"`
	// InternalLinkingEnabled is the triggering ScheduledArticle's per-article
	// internal-linking override. Nil means "inherit the WebEntity default",
	// resolved against the web entity when the master context is created.
	InternalLinkingEnabled *bool `json:"internalLinkingEnabled,omitempty"`
	// ThumbnailStyle is the triggering ScheduledArticle's per-article thumbnail
	// style override. Nil means "inherit the WebEntity default", resolved (and
	// validated against the catalog) when the master context is created.
	ThumbnailStyle *string `json:"thumbnailStyle,omitempty"`
}

// CGETavilySearchPayload carries the search variant for fan-out Tavily searches.
type CGETavilySearchPayload struct {
	WebEntityMasterContextID string `json:"webEntityMasterContextId"`
	Variant                  string `json:"variant"` // "news", "expert", "mistakes"
	Query                    string `json:"query"`
}

// CGEUrlScrapePayload carries the URL to scrape for a single fan-out message.
type CGEUrlScrapePayload struct {
	WebEntityMasterContextID string `json:"webEntityMasterContextId"`
	URL                      string `json:"url"`
}

// CGEYouTubeTranscriptPayload carries the video to transcribe for a single fan-out message.
type CGEYouTubeTranscriptPayload struct {
	WebEntityMasterContextID string `json:"webEntityMasterContextId"`
	VideoID                  string `json:"videoId"`
	Title                    string `json:"title"`
	Channel                  string `json:"channel"`
}

// CGEStandardPayload is the standard payload for CGE sub-processes that need only the master context ID.
type CGEStandardPayload struct {
	WebEntityMasterContextID string `json:"webEntityMasterContextId"`
}

// CGEImageGenerationPayload carries the image position for the fan-out image generation step.
type CGEImageGenerationPayload struct {
	WebEntityMasterContextID string `json:"webEntityMasterContextId"`
	Position                 string `json:"position"` // "thumbnail" or "mid-article"
}

type ContentGenerationService interface {
	Orchestrate(ctx context.Context, userId string, payload CGEOrchestratePayload) error
	HandleSerpFetch(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleUrlScrape(ctx context.Context, userId string, payload CGEUrlScrapePayload) error
	HandleSerpGapAnalysis(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleTavilySearch(ctx context.Context, userId string, payload CGETavilySearchPayload) error
	HandleYouTubeSearch(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleYouTubeTranscript(ctx context.Context, userId string, payload CGEYouTubeTranscriptPayload) error
	HandleYouTubeSummary(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleOutlineGeneration(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleArticleGeneration(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleInternalLinkInsertion(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleImageGeneration(ctx context.Context, userId string, payload CGEImageGenerationPayload) error
	HandleImageReplacement(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleSchemaGeneration(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleMetaAssetsGeneration(ctx context.Context, userId string, payload CGEStandardPayload) error
	HandleFinalAssembly(ctx context.Context, userId string, payload CGEStandardPayload) error

	// RegenerateSection synchronously rewrites one user-selected passage of a
	// generated article. The article body is NOT mutated — the validated
	// rewrite is recorded as a pending entry on the master context (so it
	// reliably survives reloads/devices) and returned; the client splices it
	// into the editor and commits through the normal draft-save. Returns
	// ErrRegenArticleNotFound / ErrRegenNotGenerated / ErrRegenInvalidInput /
	// ErrRegenUnsafeOutput as applicable.
	RegenerateSection(ctx context.Context, userId string, req RegenerateSectionRequest) (*RegeneratedSection, error)

	// RegenerateImage synchronously generates a replacement for one image slot
	// to a fresh versioned S3 key, recorded as the position's pending entry on
	// the master context. It deliberately does NOT update the stored image
	// record, done-flags, or the pipeline fan-in — the swap is committed only
	// at save-draft time so the old image survives for undo. Same error
	// contract as RegenerateSection.
	RegenerateImage(ctx context.Context, userId string, req RegenerateImageRequest) (*RegeneratedImage, error)
}
