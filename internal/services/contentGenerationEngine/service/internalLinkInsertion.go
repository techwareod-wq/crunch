package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"github.com/atharva-ng/crunch/internal/util/log"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

// sitemapImagePlaceholders are the tokens that must survive the link-insertion
// round-trip — they're replaced by the later image steps.
var internalLinkPlaceholders = []string{models.PlaceholderImageThumbnail, models.PlaceholderImageMidArticle}

// internalLinkInsertionResponse is the JSON contract for the link-insertion
// LLM call. Only ArticleMarkdown is consumed; LinksAdded is parsed for
// observability and forward-compatibility.
type internalLinkInsertionResponse struct {
	ArticleMarkdown string `json:"article_markdown"`
	LinksAdded      []struct {
		URL        string `json:"url"`
		AnchorText string `json:"anchor_text"`
		Reason     string `json:"reason"`
	} `json:"links_added"`
}

// HandleInternalLinkInsertion runs after article generation and before image
// generation. When enabled for the article, it fetches the business's XML
// sitemap and asks an LLM to weave 2-5 relevant internal links into the
// finished markdown. The step is strictly best-effort: a disabled toggle, a
// missing/unreachable sitemap, an LLM error, or an invalid/placeholder-dropping
// response all leave the article untouched. Either way the step is marked done
// and the pipeline advances to image generation, so internal linking can never
// stall or fail content generation.
func (s *contentGenerationEngineService) HandleInternalLinkInsertion(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("internal link insertion: %w", err)
	}

	update := models.WEMCUpdateReq{
		ProcessMetadata: &models.CGEProcessMetadata{
			PostArticleStatus: models.CGEPostArticleStatus{InternalLinkInsertionDone: true},
		},
	}

	if mc.InternalLinkingEnabled && mc.ArticleContent != "" && mc.WebsiteURL != "" {
		if updated, ok := s.insertInternalLinks(ctx, mc, kw); ok {
			wordCount := models.CountArticleWords(updated)
			update.ArticleContent = &updated
			update.WordCount = &wordCount
		}
	}

	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, update); err != nil {
		return fmt.Errorf("internal link insertion: save result: %w", err)
	}

	dc := pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID,
	}
	return s.pipeline.DispatchNext(ctx, cge.ProcessCGEInternalLinkInsertion, dc)
}

// insertInternalLinks performs the sitemap fetch + LLM call and returns the
// link-enriched markdown. The bool is false (and the original article should be
// kept) whenever anything goes wrong or the LLM returns nothing usable.
func (s *contentGenerationEngineService) insertInternalLinks(ctx context.Context, mc *models.WebEntityMasterContext, kw *models.Keyword) (string, bool) {
	sitemapURLs := commonutils.FetchSitemapURLs(ctx, mc.WebsiteURL, commonutils.SitemapFetchOptions{
		FetchTimeout: time.Duration(s.values.InternalLinks.FetchTimeoutSeconds) * time.Second,
		MaxBytes:     s.values.InternalLinks.MaxSitemapBytes,
		MaxURLs:      s.values.InternalLinks.MaxSitemapURLs,
	})
	if len(sitemapURLs) == 0 {
		// No sitemap reachable — continue without internal links.
		return "", false
	}

	promptData := cgeDto.InternalLinkInsertionPrompt{
		Keyword:         kw.Keyword,
		Title:           mc.ProposedTitle,
		WebsiteURL:      mc.WebsiteURL,
		SitemapURLs:     strings.Join(sitemapURLs, "\n"),
		ArticleMarkdown: mc.ArticleContent,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.InternalLinkInsertion, promptData)
	if err != nil {
		log.Error("internal link insertion: construct prompt", "error", err)
		return "", false
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.values.ArticleMaxTokens,
	})
	if err != nil {
		log.Error("internal link insertion: LLM call", "error", err)
		return "", false
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		log.Error("internal link insertion: clean response", "error", err)
		return "", false
	}

	var parsed internalLinkInsertionResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		// Invalid JSON — keep the original article unchanged.
		log.Warn("internal link insertion: invalid JSON response, keeping original", "error", err)
		return "", false
	}

	if strings.TrimSpace(parsed.ArticleMarkdown) == "" {
		return "", false
	}

	// Safety net behind the prompt's own rule: never accept a response that
	// dropped an image placeholder, or the later image steps lose their anchor.
	if !preservesImagePlaceholders(mc.ArticleContent, parsed.ArticleMarkdown) {
		log.Warn("internal link insertion: response altered image placeholders, keeping original")
		return "", false
	}

	return parsed.ArticleMarkdown, true
}

// preservesImagePlaceholders reports whether every image placeholder appears
// the same number of times in modified as in original — the gate that protects
// the later image-replacement step's anchors.
func preservesImagePlaceholders(original, modified string) bool {
	for _, ph := range internalLinkPlaceholders {
		if strings.Count(original, ph) != strings.Count(modified, ph) {
			return false
		}
	}
	return true
}
