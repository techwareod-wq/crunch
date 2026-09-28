package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
)

func (s *contentGenerationEngineService) HandleImageReplacement(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	ok, mc, err := s.store.GetWebEntityMasterContext(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("image replacement: get master context: %w", err)
	}
	if !ok {
		return fmt.Errorf("image replacement: master context not found: %s", masterContextID)
	}

	article := mc.ArticleContent

	// Only the mid-article image is embedded inline in the body. The thumbnail
	// and the title (H1) are stored separately (mc.Images / ProposedTitle) and
	// re-composed per consumer, so the thumbnail placeholder is dropped rather
	// than resolved, and StripArticleTitleAndThumbnail then removes it along with
	// the leading H1 to leave a clean body.
	for _, img := range mc.Images {
		if img.Position != models.ImagePositionMidArticle {
			continue
		}
		imageURL := s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, img.S3Key)
		markdown := fmt.Sprintf("![%s](%s)", img.Alt, imageURL)
		article = strings.ReplaceAll(article, models.PlaceholderImageMidArticle, markdown)
	}
	_, article = models.StripArticleTitleAndThumbnail(article)

	wordCount := models.CountArticleWords(article)
	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		ArticleContent: &article,
		WordCount:      &wordCount,
		ProcessMetadata: &models.CGEProcessMetadata{
			PostArticleStatus: models.CGEPostArticleStatus{
				ImageReplacementDone: true,
			},
		},
	}); err != nil {
		return fmt.Errorf("image replacement: save: %w", err)
	}

	dc := pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID,
	}
	return s.pipeline.DispatchNext(ctx, cge.ProcessCGEImageReplacement, dc)
}
