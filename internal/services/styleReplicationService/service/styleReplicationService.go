package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type styleReplicationService struct {
	LLM        *config.LLMProvider
	dfs        interfaces.DataForSEO
	dispatcher interfaces.Dispatcher
	s3         interfaces.S3
	s3Bucket   string
	values     config.StyleReplicationValues
	dfsValues  config.DataForSEOValues
}

func NewService(
	llm *config.LLMProvider,
	dfs interfaces.DataForSEO,
	dispatcher interfaces.Dispatcher,
	s3 interfaces.S3,
	s3Bucket string,
	values config.StyleReplicationValues,
	dfsValues config.DataForSEOValues,
) sr.StyleReplicationService {
	return &styleReplicationService{
		LLM:        llm,
		dfs:        dfs,
		dispatcher: dispatcher,
		s3:         s3,
		s3Bucket:   s3Bucket,
		values:     values,
		dfsValues:  dfsValues,
	}
}

// styleImageContentTypeExt mirrors the scheduled-article upload allowlist:
// raster formats the review grid and the vision call can both consume.
var styleImageContentTypeExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// styleUploadKeyPrefix is the per-user/run namespace uploaded reference
// images live under. Approve validates returned keys against it so a client
// can never point the run at an arbitrary bucket object.
func styleUploadKeyPrefix(userID, runID string) string {
	return fmt.Sprintf("style_replication/uploads/%s-%s", userID, runID)
}

// validStyleImagePosition guards the two reference-image buckets.
func validStyleImagePosition(position string) bool {
	return position == models.ImagePositionThumbnail || position == models.ImagePositionMidArticle
}

// CreateReferenceImageUploadURL issues the presigned PUT + public URL for one
// user-uploaded reference image in one position bucket. Only meaningful while
// the run awaits review — any other state means the client is stale.
func (s *styleReplicationService) CreateReferenceImageUploadURL(ctx context.Context, userID string, run *models.StyleReplicationRun, position, contentType string) (*sr.StyleImageUploadTarget, error) {
	ext, ok := styleImageContentTypeExt[strings.ToLower(strings.TrimSpace(contentType))]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported image content type %q", sr.ErrStyleRunInvalidInput, contentType)
	}
	if !validStyleImagePosition(position) {
		return nil, fmt.Errorf("%w: unknown image position %q", sr.ErrStyleRunInvalidInput, position)
	}
	if !run.Learn.WantsPosition(position) {
		return nil, fmt.Errorf("%w: this run isn't learning the %s style", sr.ErrStyleRunInvalidInput, position)
	}
	if run.Status != models.StyleRunStatusAwaitingReview {
		return nil, sr.ErrStyleRunWrongState
	}

	// Unix-nano suffix keeps concurrent uploads for the same run collision-free.
	key := fmt.Sprintf("%s-%s-%d.%s", styleUploadKeyPrefix(userID, run.ID.Hex()), position, time.Now().UnixNano(), ext)

	presigned, err := s.s3.GenerateSinglepartUploadPresignedURLs(ctx, s.s3Bucket, []dto.PresignedSinglepartPutRequest{
		{Key: key, ContentType: contentType},
	})
	if err != nil {
		return nil, fmt.Errorf("style replication: presign reference image upload: %w", err)
	}
	if len(presigned) != 1 {
		return nil, fmt.Errorf("style replication: presign reference image upload: expected 1 url, got %d", len(presigned))
	}

	return &sr.StyleImageUploadTarget{
		Key:       presigned[0].Key,
		UploadURL: presigned[0].URL,
		Headers:   presigned[0].Headers,
		PublicURL: s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, key),
	}, nil
}

// StartRun guards the rate limit + single-active-run invariant, creates the
// run, and dispatches discovery. The active-run check is find-then-create (no
// unique index backs it) — a double-clicked start can slip through, which
// costs a redundant discovery, nothing worse.
func (s *styleReplicationService) StartRun(ctx context.Context, userID string, entity *models.WebEntity) (*models.StyleReplicationRun, error) {
	if found, _, err := models.FindActiveStyleReplicationRunForEntity(ctx, entity.ID); err != nil {
		return nil, fmt.Errorf("style replication: check active run: %w", err)
	} else if found {
		return nil, sr.ErrStyleRunActive
	}

	// Rolling 24h window; errored runs are excluded by the count itself.
	count, err := models.CountStyleReplicationRunsSince(ctx, entity.ID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("style replication: rate limit count: %w", err)
	}
	if count >= int64(s.values.MaxRunsPerDay) {
		return nil, sr.ErrStyleRunRateLimited
	}

	userOID, err := parseObjectID(userID)
	if err != nil {
		return nil, fmt.Errorf("style replication: invalid user id: %w", err)
	}

	run := &models.StyleReplicationRun{
		WebEntityID: entity.ID,
		CompanyID:   entity.CompanyID,
		UserID:      userOID,
		Status:      models.StyleRunStatusCreated,
	}
	if err := models.CreateStyleReplicationRun(ctx, run); err != nil {
		return nil, fmt.Errorf("style replication: create run: %w", err)
	}

	if err := s.dispatcher.Dispatch(ctx, string(sr.ProcessStyleDiscovery), userID, sr.StyleRunPayload{RunID: run.ID.Hex()}); err != nil {
		if markErr := models.SetStyleRunError(ctx, run.ID.Hex(), "discovery dispatch failed"); markErr != nil {
			log.Error("style replication: mark run error after dispatch failure", "error", markErr, "runId", run.ID.Hex())
		}
		return nil, fmt.Errorf("style replication: dispatch discovery: %w", err)
	}
	return run, nil
}

// SubmitURLs finalizes the source list + learn selection and fans the scrape
// out, one SQS message per URL. The awaiting_urls→scraping CAS makes a
// double-posted submit a no-op instead of a double fan-out.
func (s *styleReplicationService) SubmitURLs(ctx context.Context, userID string, run *models.StyleReplicationRun, urls []string, learn models.StyleLearnSelection) error {
	if !learn.Any() {
		return fmt.Errorf("%w: select at least one artifact to learn", sr.ErrStyleRunInvalidInput)
	}
	cleaned, err := validateSourceURLs(urls, s.values.MinUrls, s.values.MaxUrls)
	if err != nil {
		return err
	}

	statuses := make([]models.UrlScrapeStatus, 0, len(cleaned))
	for _, u := range cleaned {
		statuses = append(statuses, models.UrlScrapeStatus{URL: u})
	}

	won, err := models.TryAdvanceStyleRunStatus(ctx, run.ID.Hex(),
		[]int{models.StyleRunStatusAwaitingURLs},
		models.StyleRunStatusScraping,
		map[string]interface{}{
			"scrape_statuses":   statuses,
			"scrapes_remaining": len(statuses),
			"learn":             learn,
		})
	if err != nil {
		return fmt.Errorf("style replication: start scraping: %w", err)
	}
	if !won {
		return sr.ErrStyleRunWrongState
	}

	for _, u := range cleaned {
		if err := s.dispatcher.Dispatch(ctx, string(sr.ProcessStyleScrape), userID, sr.StyleScrapePayload{
			RunID: run.ID.Hex(),
			URL:   u,
		}); err != nil {
			if markErr := models.SetStyleRunError(ctx, run.ID.Hex(), "scrape dispatch failed"); markErr != nil {
				log.Error("style replication: mark run error after scrape dispatch failure", "error", markErr, "runId", run.ID.Hex())
			}
			return fmt.Errorf("style replication: dispatch scrape for %s: %w", u, err)
		}
	}
	return nil
}

// Approve applies the review deselections, registers any user-uploaded
// reference images, and dispatches synthesis. The awaiting_review→synthesizing
// CAS keeps a double-posted approve from double-dispatching.
func (s *styleReplicationService) Approve(ctx context.Context, userID string, run *models.StyleReplicationRun, deselectedImageURLs []string, uploadedImages []sr.StyleUploadedImageRef) error {
	uploadedRefs, err := validateUploadedRefs(uploadedImages, styleUploadKeyPrefix(userID, run.ID.Hex()), s.values.MaxSynthesisImages)
	if err != nil {
		return err
	}

	// Uploads may only target buckets this run is actually learning.
	for _, ref := range uploadedRefs {
		if !run.Learn.WantsPosition(ref.Position) {
			return fmt.Errorf("%w: uploaded image targets a bucket this run isn't learning (%s)", sr.ErrStyleRunInvalidInput, ref.Position)
		}
	}

	// An image-only run whose every image was deselected AND that brings no
	// uploads has nothing left to learn — reject before consuming the run.
	// (ScrapedImages only ever holds requested buckets, so the plain count is
	// already selection-scoped.)
	if !run.Learn.Articles() && run.Learn.Images() && len(uploadedRefs) == 0 {
		deselected := map[string]bool{}
		for _, u := range deselectedImageURLs {
			deselected[u] = true
		}
		remaining := 0
		for _, img := range run.ScrapedImages {
			if img.Selected && !deselected[img.URL] {
				remaining++
			}
		}
		if remaining == 0 {
			return fmt.Errorf("%w: every image was deselected and no text artifact is being learned", sr.ErrStyleRunInvalidInput)
		}
	}

	won, err := models.TryAdvanceStyleRunStatus(ctx, run.ID.Hex(),
		[]int{models.StyleRunStatusAwaitingReview},
		models.StyleRunStatusSynthesizing, nil)
	if err != nil {
		return fmt.Errorf("style replication: start synthesis: %w", err)
	}
	if !won {
		return sr.ErrStyleRunWrongState
	}

	if err := models.DeselectStyleRunImages(ctx, run.ID.Hex(), deselectedImageURLs); err != nil {
		return fmt.Errorf("style replication: apply deselections: %w", err)
	}

	if len(uploadedRefs) > 0 {
		uploads := make([]models.StyleScrapedImage, 0, len(uploadedRefs))
		for _, ref := range uploadedRefs {
			uploads = append(uploads, models.StyleScrapedImage{
				URL:      s.s3.GetS3UrlFromBucketAndFileName(s.s3Bucket, ref.Key),
				Position: ref.Position,
				S3Key:    ref.Key,
				Selected: true,
			})
		}
		if err := models.AppendStyleRunUploadedImages(ctx, run.ID.Hex(), uploads); err != nil {
			return fmt.Errorf("style replication: register uploaded images: %w", err)
		}
	}

	if err := s.dispatcher.Dispatch(ctx, string(sr.ProcessStyleSynthesis), userID, sr.StyleRunPayload{RunID: run.ID.Hex()}); err != nil {
		if markErr := models.SetStyleRunError(ctx, run.ID.Hex(), "synthesis dispatch failed"); markErr != nil {
			log.Error("style replication: mark run error after synthesis dispatch failure", "error", markErr, "runId", run.ID.Hex())
		}
		return fmt.Errorf("style replication: dispatch synthesis: %w", err)
	}
	return nil
}

// Cancel abandons an active run by parking it in the terminal error state
// with a canceled marker. Error runs don't count toward the daily rate limit,
// so canceling never burns a slot. Stale in-flight workers see the non-active
// status and no-op.
func (s *styleReplicationService) Cancel(ctx context.Context, run *models.StyleReplicationRun) error {
	return models.SetStyleRunError(ctx, run.ID.Hex(), "canceled by user")
}

// validateUploadedRefs checks every user-supplied upload against the run's
// key namespace (a foreign key could point synthesis at an arbitrary bucket
// object) and a known position, and caps each position bucket at the vision
// budget (each bucket feeds its own call). Dedupes by key, preserves order.
func validateUploadedRefs(refs []sr.StyleUploadedImageRef, prefix string, maxPerPosition int) ([]sr.StyleUploadedImageRef, error) {
	seen := map[string]bool{}
	perPosition := map[string]int{}
	out := make([]sr.StyleUploadedImageRef, 0, len(refs))
	for _, ref := range refs {
		ref.Key = strings.TrimSpace(ref.Key)
		if ref.Key == "" || seen[ref.Key] {
			continue
		}
		if !strings.HasPrefix(ref.Key, prefix) {
			return nil, fmt.Errorf("%w: uploaded image key outside this run's namespace", sr.ErrStyleRunInvalidInput)
		}
		if !validStyleImagePosition(ref.Position) {
			return nil, fmt.Errorf("%w: unknown image position %q", sr.ErrStyleRunInvalidInput, ref.Position)
		}
		seen[ref.Key] = true
		perPosition[ref.Position]++
		out = append(out, ref)
	}
	for position, n := range perPosition {
		if n > maxPerPosition {
			return nil, fmt.Errorf("%w: at most %d uploaded %s images are allowed", sr.ErrStyleRunInvalidInput, maxPerPosition, position)
		}
	}
	return out, nil
}

// validateSourceURLs normalizes and validates the finalized URL list: min-max
// entries (min floors at 1), each an absolute http(s) URL. Cross-domain URLs
// are allowed by design (competitor cloning) — no domain checks beyond
// well-formedness.
func validateSourceURLs(urls []string, min, max int) ([]string, error) {
	if min < 1 {
		min = 1
	}
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(urls))
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w: %q is not an absolute http(s) URL", sr.ErrStyleRunInvalidInput, raw)
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		cleaned = append(cleaned, raw)
	}
	if len(cleaned) < min {
		return nil, fmt.Errorf("%w: at least %d URL(s) are required", sr.ErrStyleRunInvalidInput, min)
	}
	if len(cleaned) > max {
		return nil, fmt.Errorf("%w: at most %d URLs are allowed", sr.ErrStyleRunInvalidInput, max)
	}
	return cleaned, nil
}
