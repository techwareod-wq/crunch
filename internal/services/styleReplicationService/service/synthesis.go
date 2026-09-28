package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	srDto "github.com/atharva-ng/crunch/internal/services/styleReplicationService/dto"
	"github.com/atharva-ng/crunch/internal/services/styleReplicationService/prompts"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
)

const (
	// visionMaxDim is the long-edge cap for downscaled synthesis images.
	visionMaxDim = 1024
	// maxImageFetchBytes caps one fetched image body.
	maxImageFetchBytes = 10 << 20
	// imageFetchUserAgent mirrors the scrape UA — hotlink guards keyed on the
	// default Go agent would otherwise starve the vision call.
	imageFetchUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// HandleSynthesis runs the LLM synthesis over the approved scrape results and
// stamps the artifacts onto the WebEntity. Article learning and image learning
// are independent — either may be off or may have yielded nothing; producing
// at least one artifact counts as success (overwrite-on-success semantics).
func (s *styleReplicationService) HandleSynthesis(ctx context.Context, userID string, payload sr.StyleRunPayload) error {
	found, run, err := models.GetStyleReplicationRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("style synthesis: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("style synthesis: run not found: %s", payload.RunID)
	}
	if run.Status != models.StyleRunStatusSynthesizing {
		return nil // canceled or already completed — redelivery no-op
	}

	foundEntity, entity, err := models.FindWebEntityByID(ctx, run.WebEntityID.Hex())
	if err != nil {
		return fmt.Errorf("style synthesis: load web entity: %w", err)
	}
	if !foundEntity {
		return fmt.Errorf("style synthesis: web entity not found: %s", run.WebEntityID.Hex())
	}

	var artifacts models.StyleRunArtifacts

	if run.Learn.Articles() && len(run.ScrapedContents) > 0 {
		artifacts.ToneProfile, artifacts.StructurePattern, artifacts.TitlePattern, err = s.synthesizeToneStructure(ctx, run.ScrapedContents)
		if err != nil {
			return fmt.Errorf("style synthesis: tone/structure: %w", err)
		}
		// The text call derives all three in one pass; a partial selection
		// (e.g. a title-only re-run) masks the unrequested ones so the
		// merge-on-write below can't overwrite artifacts the user didn't ask
		// to re-learn.
		if !run.Learn.ToneProfile {
			artifacts.ToneProfile = ""
		}
		if !run.Learn.StructurePattern {
			artifacts.StructurePattern = ""
		}
		if !run.Learn.TitlePattern {
			artifacts.TitlePattern = ""
		}
	}

	// The two positions learn independently — thumbnails from og:image / hero
	// references, mid-article from in-article imagery — each through its own
	// vision call, and each only when its bucket was selected.
	if run.Learn.ThumbnailStyle {
		if thumbs := selectedImages(run.ScrapedImages, models.ImagePositionThumbnail, s.values.MaxSynthesisImages); len(thumbs) > 0 {
			artifacts.ThumbnailStylePrompt, err = s.synthesizeImageStyle(ctx, thumbs, "blog thumbnail / hero", prompts.ThumbnailStyleSynthesis)
			if err != nil {
				return fmt.Errorf("style synthesis: thumbnail style: %w", err)
			}
		}
	}
	if run.Learn.MidArticleStyle {
		if mids := selectedImages(run.ScrapedImages, models.ImagePositionMidArticle, s.values.MaxSynthesisImages); len(mids) > 0 {
			artifacts.MidArticleStylePrompt, err = s.synthesizeImageStyle(ctx, mids, "mid-article illustration", prompts.MidArticleStyleSynthesis)
			if err != nil {
				return fmt.Errorf("style synthesis: mid-article style: %w", err)
			}
		}
	}

	if artifacts.Empty() {
		return models.SetStyleRunError(ctx, payload.RunID, "synthesis produced no artifacts")
	}

	// Overwrite the artifacts this run learned; keep the others so an
	// image-only re-run doesn't wipe a previously learned tone.
	profile := models.StyleReplication{}
	if existing := entity.StyleProfile(); existing != nil {
		profile = *existing
	}
	if artifacts.ToneProfile != "" {
		profile.ToneProfile = &artifacts.ToneProfile
	}
	if artifacts.StructurePattern != "" {
		profile.StructurePattern = &artifacts.StructurePattern
	}
	if artifacts.TitlePattern != "" {
		profile.TitlePattern = &artifacts.TitlePattern
	}
	if artifacts.ThumbnailStylePrompt != "" {
		profile.ThumbnailStylePrompt = &artifacts.ThumbnailStylePrompt
	}
	if artifacts.MidArticleStylePrompt != "" {
		profile.MidArticleStylePrompt = &artifacts.MidArticleStylePrompt
	}
	profile.SourceURLs = successfulURLs(run.ScrapeStatuses)
	now := time.Now()
	profile.LearnedAt = &now

	if err := models.SetWebEntityStyleReplication(ctx, entity.ID, profile); err != nil {
		return fmt.Errorf("style synthesis: save profile: %w", err)
	}
	if err := models.CompleteStyleReplicationRun(ctx, payload.RunID, artifacts); err != nil {
		return fmt.Errorf("style synthesis: complete run: %w", err)
	}

	// User-uploaded reference images served their purpose — delete them
	// best-effort AFTER the completion write so an SQS retry (which reloads
	// the run and re-fetches) never races a deleted object. Canceled/errored
	// runs leave orphans, same accepted policy as abandoned article uploads.
	for _, img := range run.ScrapedImages {
		if img.S3Key == "" {
			continue
		}
		if err := s.s3.DeleteFile(ctx, s.s3Bucket, img.S3Key); err != nil {
			log.Warn("style synthesis: uploaded reference image cleanup failed", "key", img.S3Key, "error", err)
		}
	}
	return nil
}

// synthesizeToneStructure runs the one-call tone + structure + title-pattern
// derivation over every scraped article text. The titles are ALSO listed as
// their own block: title style (casing, shape, punctuation habits) is a
// different signal from body voice, and burying titles inside the article
// dumps made the model generalize from bodies alone.
func (s *styleReplicationService) synthesizeToneStructure(ctx context.Context, contents []models.StyleScrapedContent) (string, string, string, error) {
	var titles strings.Builder
	var b strings.Builder
	for i, c := range contents {
		if c.Title != "" {
			fmt.Fprintf(&titles, "%d. %s\n", i+1, c.Title)
		}
		fmt.Fprintf(&b, "=== ARTICLE %d: %s ===\n", i+1, c.URL)
		if c.Title != "" {
			fmt.Fprintf(&b, "Title: %s\n", c.Title)
		}
		b.WriteString("\n")
		b.WriteString(c.Content)
		b.WriteString("\n\n")
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.ToneStructureSynthesis, srDto.ToneStructureSynthesisPrompt{
		ArticleCount: len(contents),
		Titles:       strings.TrimSpace(titles.String()),
		Articles:     b.String(),
	})
	if err != nil {
		return "", "", "", fmt.Errorf("construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return "", "", "", fmt.Errorf("clean response: %w", err)
	}
	var parsed srDto.ToneStructureSynthesisResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return "", "", "", fmt.Errorf("unmarshal response: %w", err)
	}
	tone := strings.TrimSpace(parsed.ToneProfile)
	structure := strings.TrimSpace(parsed.StructurePattern)
	titlePattern := strings.TrimSpace(parsed.TitlePattern)
	if tone == "" && structure == "" && titlePattern == "" {
		return "", "", "", fmt.Errorf("LLM returned empty artifacts")
	}
	return tone, structure, titlePattern, nil
}

// synthesizeImageStyle fetches one position bucket's approved images
// server-side (the only time image bytes are pulled — review hotlinks the
// originals), downscales them, and runs the vision call with that position's
// template — thumbnails synthesize text-free rules, mid-article may learn
// typography/label/diagram habits, mirroring the generation templates.
// Unfetchable/undecodable images are skipped; the call proceeds with whatever
// survived.
func (s *styleReplicationService) synthesizeImageStyle(ctx context.Context, images []models.StyleScrapedImage, imageKind, template string) (string, error) {
	client := &http.Client{Timeout: fetchTimeout(s.values.FetchTimeoutSeconds)}

	var blocks []dto.ImageBlock
	for _, img := range images {
		data, err := fetchImageBytes(ctx, client, img.URL)
		if err != nil {
			log.Warn("style synthesis: image fetch failed, skipping", "url", img.URL, "error", err)
			continue
		}
		encoded, mediaType, err := utils.PrepareVisionImage(data, visionMaxDim)
		if err != nil {
			log.Warn("style synthesis: image unusable for vision, skipping", "url", img.URL, "error", err)
			continue
		}
		blocks = append(blocks, dto.ImageBlock{
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(encoded),
		})
	}
	if len(blocks) == 0 {
		log.Warn("style synthesis: no approved image survived fetching — skipping image style", "imageKind", imageKind)
		return "", nil
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(template, srDto.ImageStyleSynthesisPrompt{
		ImageCount: len(blocks),
		ImageKind:  imageKind,
	})
	if err != nil {
		return "", fmt.Errorf("construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages:  []dto.Message{{Role: "user", Content: prompt, Images: blocks}},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("vision call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return "", fmt.Errorf("clean response: %w", err)
	}
	var parsed srDto.ImageStyleSynthesisResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	return strings.TrimSpace(parsed.ImageStylePrompt), nil
}

func fetchImageBytes(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", imageFetchUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image fetch %s: status %d", target, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxImageFetchBytes))
}

func selectedImages(images []models.StyleScrapedImage, position string, max int) []models.StyleScrapedImage {
	seen := map[string]bool{}
	var out []models.StyleScrapedImage
	for _, img := range images {
		if img.Position != position {
			continue
		}
		// Dedupe by URL — the same image scraped off several source pages
		// must not spend two vision slots.
		if !img.Selected || seen[img.URL] {
			continue
		}
		seen[img.URL] = true
		out = append(out, img)
		if len(out) >= max {
			break
		}
	}
	return out
}

func successfulURLs(statuses []models.UrlScrapeStatus) []string {
	var out []string
	for _, st := range statuses {
		if st.Done {
			out = append(out, st.URL)
		}
	}
	return out
}

func fetchTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 45 * time.Second
	}
	return time.Duration(seconds) * time.Second
}
