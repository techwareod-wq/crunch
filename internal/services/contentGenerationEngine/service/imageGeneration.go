package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for image.Decode
	_ "image/jpeg" // register JPEG decoder for image.Decode
	"image/png"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *contentGenerationEngineService) HandleImageGeneration(ctx context.Context, userId string, payload cge.CGEImageGenerationPayload) error {
	masterContextID := payload.WebEntityMasterContextID
	position := payload.Position

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("image generation [%s]: %w", position, err)
	}

	promptData := buildImagePromptData(mc, kw, position, s.values.MidArticleSectionMaxChars)

	promptTemplate := imagePromptTemplateForPosition(position, mc.ThumbnailStyle, mc.ThumbnailStylePrompt, mc.MidArticleStylePrompt)
	finalPrompt, err := s.LLM.Utils.ConstructPrompt(promptTemplate, promptData)
	if err != nil {
		return fmt.Errorf("image generation [%s]: construct prompt: %w", position, err)
	}

	alt := buildImageAltText(promptData, position, s.values.AltTextMaxChars)

	// Persist the final image prompt for observability.
	if err := s.store.SetCGEImageGenPrompt(ctx, masterContextID, position, models.CGEImageGenPrompt{
		Prompt:      finalPrompt,
		ImagePrompt: finalPrompt,
		AltText:     alt,
	}); err != nil {
		return fmt.Errorf("image generation [%s]: save image prompt: %w", position, err)
	}

	imgResp, err := s.imageGen.GenerateImage(ctx, interfaces.ImageGenRequest{
		Prompt:      finalPrompt,
		AspectRatio: s.values.ImageAspectRatio,
	})
	if err != nil {
		return fmt.Errorf("image generation [%s]: image provider call: %w", position, err)
	}

	s3Key, err := s.uploadImageToS3(ctx, imgResp.Data, userId, masterContextID, position)
	if err != nil {
		return fmt.Errorf("image generation [%s]: upload to s3: %w", position, err)
	}

	if alt == "" {
		alt = "Article image"
	}
	if err := s.store.SetCGEImageByPosition(ctx, masterContextID, models.CGEImage{
		Position: position,
		S3Key:    s3Key,
		Alt:      alt,
	}); err != nil {
		return fmt.Errorf("image generation [%s]: save image: %w", position, err)
	}

	statusFlag := models.CGEProcessMetadata{}
	if position == models.ImagePositionThumbnail {
		statusFlag.PostArticleStatus.ImageGenThumbnailDone = true
	} else {
		statusFlag.PostArticleStatus.ImageGenMidArticleDone = true
	}
	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		ProcessMetadata: &statusFlag,
	}); err != nil {
		return fmt.Errorf("image generation [%s]: mark done: %w", position, err)
	}

	return s.checkImageGenCompletionAndDispatchReplacement(ctx, masterContextID, userId)
}

func (s *contentGenerationEngineService) checkImageGenCompletionAndDispatchReplacement(ctx context.Context, masterContextID, userID string) error {
	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("image fan-in: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("image fan-in: master context not found: %s", masterContextID)
	}

	if !mc.ProcessMetadata.PostArticleStatus.AllImageGenDone() {
		return nil
	}

	won, err := s.store.AtomicClaimImageReplacementDispatch(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("image fan-in: atomic claim: %w", err)
	}
	if !won {
		return nil
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEImageReplacement), userID, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}

func buildImagePromptData(mc *models.WebEntityMasterContext, kw *models.Keyword, position string, sectionMaxChars int) cgeDto.ImagePromptGenerationPrompt {
	p := cgeDto.ImagePromptGenerationPrompt{
		Position: position,
		Keyword:  kw.Keyword,
	}
	// Each position renders its own learned style rules.
	if position == models.ImagePositionThumbnail {
		p.ImageStyle = mc.ThumbnailStylePrompt
	} else {
		p.ImageStyle = mc.MidArticleStylePrompt
	}

	if mc.Outline != nil {
		var outline map[string]interface{}
		if err := json.Unmarshal([]byte(mc.Outline.RawJSON), &outline); err == nil {
			if h1, ok := outline["h1"].(string); ok {
				p.H1 = h1
			}
		}
	}

	if bc := mc.BusinessContext; bc != nil {
		p.BrandVoiceTone = commonutils.Deref(bc.BrandVoiceSignals)
		if bc.ICPSignals != nil && len(bc.ICPSignals.Roles) > 0 {
			p.ICPRole = strings.Join(bc.ICPSignals.Roles, ", ")
		}
	}

	if position == models.ImagePositionThumbnail {
		p.ArticleIntroduction = extractFirstParagraph(mc.ArticleContent)
	} else {
		p.SectionTitle, p.SectionContent = extractMidArticleSection(mc.ArticleContent, sectionMaxChars)
	}

	return p
}

// extractFirstParagraph returns the first non-empty, non-heading paragraph from markdown.
func extractFirstParagraph(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "{{") {
			continue
		}
		return trimmed
	}
	return ""
}

// extractMidArticleSection finds the {{IMAGE_MID_ARTICLE}} placeholder, then uses
// the first H2 heading *after* the placeholder as the section title (the article
// prompt places the placeholder directly above its target H2). It collects a
// bounded excerpt of that section's body — paragraphs, bullets, and metric lines —
// stopping at the next H2 heading. The excerpt is capped at maxLen characters.
func extractMidArticleSection(content string, maxLen int) (sectionTitle, sectionContent string) {
	lines := strings.Split(content, "\n")
	placeholderIdx := -1

	for i, line := range lines {
		if strings.Contains(line, models.PlaceholderImageMidArticle) {
			placeholderIdx = i
			break
		}
	}

	if placeholderIdx == -1 {
		return "", ""
	}

	// Look forwards for the first H2 after the placeholder as the section title.
	headingIdx := -1
	for i := placeholderIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "## ") {
			sectionTitle = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			headingIdx = i
			break
		}
	}

	if headingIdx == -1 {
		return sectionTitle, ""
	}

	// Collect the section body until the next H2 heading, capped to a safe length.
	var b strings.Builder
	for i := headingIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "## ") {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "{{") {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(trimmed)
		if b.Len() >= maxLen {
			break
		}
	}

	sectionContent = strings.TrimSpace(b.String())
	if len(sectionContent) > maxLen {
		sectionContent = strings.TrimSpace(sectionContent[:maxLen])
	}

	return sectionTitle, sectionContent
}

// imagePromptTemplateForPosition selects the direct image-prompt template for
// the given image position. Each position keys on ITS OWN learned style
// prompt (style replication): a stamped learned style routes to the custom
// template; for thumbnails an explicit catalog pick (non-empty
// thumbnailStyle) wins over it — resolveImageStyle stamps the fields so
// together they encode the pick → learned → default ladder. With no learned
// style this is today's behavior: catalog thumbnail template by ID, static
// mid-article template. Unknown positions fall back to mid-article.
func imagePromptTemplateForPosition(position, thumbnailStyle, thumbnailStylePrompt, midArticleStylePrompt string) string {
	switch position {
	case models.ImagePositionThumbnail:
		if thumbnailStyle == "" && thumbnailStylePrompt != "" {
			return prompts.CustomThumbnailImagePrompt
		}
		return prompts.ThumbnailTemplateForStyle(thumbnailStyle)
	default:
		if midArticleStylePrompt != "" {
			return prompts.CustomMidArticleImagePrompt
		}
		return prompts.MidArticleImagePrompt
	}
}

// buildImageAltText derives deterministic SEO alt text from the prompt context.
// No LLM call — alt text is kept short and descriptive, capped at maxLen chars.
func buildImageAltText(p cgeDto.ImagePromptGenerationPrompt, position string, maxLen int) string {
	if position == models.ImagePositionThumbnail {
		if p.H1 != "" {
			return truncateAltText("Editorial illustration for "+p.H1, maxLen)
		}
		if p.Keyword != "" {
			return truncateAltText("Editorial illustration for "+p.Keyword, maxLen)
		}
		return "Article thumbnail illustration"
	}

	if p.SectionTitle != "" {
		return truncateAltText("Illustration for "+p.SectionTitle, maxLen)
	}
	if p.Keyword != "" {
		return truncateAltText("Article section illustration for "+p.Keyword, maxLen)
	}
	return "Article section illustration"
}

// truncateAltText keeps alt text under maxLen characters, trimming at a word
// boundary where possible.
func truncateAltText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	truncated := s[:maxLen]
	if idx := strings.LastIndex(truncated, " "); idx > 0 {
		truncated = truncated[:idx]
	}
	return strings.TrimSpace(truncated)
}

// uploadImageToS3 re-encodes the provider's image as PNG and uploads it to S3,
// returning the S3 key of the uploaded object. The key is named
// "<userId>-<masterContextID>-<position>.png" — position keeps the master
// context's two images (thumbnail + mid-article) from colliding.
func (s *contentGenerationEngineService) uploadImageToS3(ctx context.Context, data []byte, userID, masterContextID, position string) (string, error) {
	pngData, err := convertToPNG(data)
	if err != nil {
		return "", fmt.Errorf("convert image to png: %w", err)
	}

	s3Key := fmt.Sprintf("generated_images/%s-%s-%s.png", userID, masterContextID, position)
	if err := s.s3.UploadFileUsingBytes(ctx, s3Key, s.s3Bucket, pngData); err != nil {
		return "", fmt.Errorf("upload to s3: %w", err)
	}

	return s3Key, nil
}

// convertToPNG decodes an encoded image (PNG/JPEG/GIF) and re-encodes it as PNG,
// guaranteeing the uploaded object is always a valid PNG regardless of the format
// the image provider returned.
func convertToPNG(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty image data")
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buf.Bytes(), nil
}
