package catalog

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Media GC (spec 03): a daily cron enqueues one sweep.
const (
	JobMediaGC     cron.JobName         = "catalog_media_gc"
	ProcessMediaGC pipeline.ProcessType = "catalog.media_gc"

	mediaGCLocalTime = "04:00"
	mediaGCCatchUp   = 6 * time.Hour
	maxFilenameLen   = 120
)

var (
	photoTypes = []string{"image/jpeg", "image/png", "image/webp"}
	docTypes   = []string{"application/pdf", "image/jpeg", "image/png", "image/webp"}
)

// MediaService handles uploads (D-015, D-062). Buckets come from config at
// call time.
type MediaService struct {
	store Store
	s3    func() interfaces.S3
	aws   func() config.AWSConfig
	links func() config.StorageValues
	cfg   func() config.CatalogValues
	now   func() time.Time
}

// UploadRequest is POST /v1/admin/media/upload-url.
type UploadRequest struct {
	WarehouseID primitive.ObjectID `json:"warehouseId"`
	Kind        string             `json:"kind"`
	DocType     string             `json:"docType"`
	Visibility  string             `json:"visibility"`
	Filename    string             `json:"filename"`
	ContentType string             `json:"contentType"`
	Bytes       int64              `json:"bytes"`
}

// UploadResponse carries the presigned PUT.
type UploadResponse struct {
	MediaID string              `json:"mediaId"`
	PutURL  string              `json:"putUrl"`
	Headers map[string][]string `json:"headers,omitempty"`
}

func (m *MediaService) limit(kind string) int64 {
	if kind == domain.MediaPhoto {
		return m.cfg().MaxPhotoBytes
	}
	return m.cfg().MaxDocBytes
}

func (m *MediaService) bucket(visibility string) string {
	if visibility == domain.VisibilityPublic {
		return m.aws().PublicBucket
	}
	return m.aws().PrivateBucket
}

// UploadURL pre-checks the upload and returns a presigned PUT. Photos are
// always public; agreements always staff (D-062).
func (m *MediaService) UploadURL(ctx context.Context, actor Actor, req UploadRequest) (UploadResponse, error) {
	if _, err := m.store.GetWarehouse(ctx, req.WarehouseID); err != nil {
		return UploadResponse{}, err
	}
	switch req.Kind {
	case domain.MediaPhoto:
		req.DocType, req.Visibility = "", domain.VisibilityPublic
		if !slices.Contains(photoTypes, req.ContentType) {
			return UploadResponse{}, invalidf("photos must be one of %s", strings.Join(photoTypes, ", "))
		}
		n, err := m.store.CountPhotos(ctx, req.WarehouseID)
		if err != nil {
			return UploadResponse{}, err
		}
		if max := m.cfg().MaxPhotos; max > 0 && n >= int64(max) {
			return UploadResponse{}, invalidf("at most %d photos per warehouse", max)
		}
	case domain.MediaDoc:
		switch req.DocType {
		case domain.DocAgreement:
			req.Visibility = domain.VisibilityStaff
		case domain.DocFloorPlan, domain.DocCertificate, domain.DocOther:
		default:
			return UploadResponse{}, invalidf("docType must be floor_plan, agreement, certificate or other")
		}
		if req.Visibility != domain.VisibilityPublic && req.Visibility != domain.VisibilityStaff {
			return UploadResponse{}, invalidf("visibility must be public or staff")
		}
		if !slices.Contains(docTypes, req.ContentType) {
			return UploadResponse{}, invalidf("docs must be one of %s", strings.Join(docTypes, ", "))
		}
	default:
		return UploadResponse{}, invalidf("kind must be photo or doc")
	}
	if req.Bytes <= 0 || req.Bytes > m.limit(req.Kind) {
		return UploadResponse{}, invalidf("file must be 1 byte – %d MB", m.limit(req.Kind)>>20)
	}
	name := cleanFilename(req.Filename)
	if name == "" {
		return UploadResponse{}, invalidf("filename is required")
	}
	bucket := m.bucket(req.Visibility)
	s3 := m.s3()
	if bucket == "" || s3 == nil {
		return UploadResponse{}, fmt.Errorf("media storage not configured")
	}
	id := primitive.NewObjectID()
	md := &domain.Media{
		ID: id, WarehouseID: req.WarehouseID, Kind: req.Kind, DocType: req.DocType, Visibility: req.Visibility,
		Bucket: bucket, Key: fmt.Sprintf("wh/%s/%s/%s", req.WarehouseID.Hex(), id.Hex(), name),
		Filename: name, ContentType: req.ContentType, Bytes: req.Bytes, Status: domain.MediaPending,
		UploadedBy: actor.Email, CreatedAt: m.now().UTC(),
	}
	urls, err := s3.GenerateSinglepartUploadPresignedURLs(ctx, bucket, []dto.PresignedSinglepartPutRequest{{Key: md.Key, ContentType: md.ContentType}})
	if err != nil || len(urls) != 1 {
		return UploadResponse{}, fmt.Errorf("presign upload: %w", err)
	}
	if err := m.store.InsertMedia(ctx, md); err != nil {
		return UploadResponse{}, err
	}
	return UploadResponse{MediaID: id.Hex(), PutURL: urls[0].URL, Headers: urls[0].Headers}, nil
}

// Confirm checks the uploaded object (HeadObject) and marks it ready. An
// oversized or wrongly typed object is deleted.
func (m *MediaService) Confirm(ctx context.Context, id primitive.ObjectID) (*domain.Media, error) {
	md, err := m.store.GetMedia(ctx, id)
	if err != nil {
		return nil, err
	}
	if md.Status == domain.MediaReady {
		return md, nil
	}
	if md.Status != domain.MediaPending {
		return nil, conflict(codeBadState, "media is %s", md.Status)
	}
	s3 := m.s3()
	if s3 == nil {
		return nil, fmt.Errorf("media storage not configured")
	}
	info, found, err := s3.HeadObject(ctx, md.Bucket, md.Key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, conflict("not_uploaded", "the file hasn't reached storage yet — upload it, then confirm")
	}
	if info.Size <= 0 || info.Size > m.limit(md.Kind) || (info.ContentType != "" && info.ContentType != md.ContentType) {
		if err := s3.DeleteFile(ctx, md.Bucket, md.Key); err != nil {
			log.Error("catalog: delete rejected upload failed", "media", id.Hex(), "error", err)
		}
		md.Status = domain.MediaOrphaned
		if err := m.store.ReplaceMedia(ctx, md); err != nil {
			return nil, err
		}
		return nil, invalidf("the uploaded file is too large or of the wrong type")
	}
	md.Status, md.Bytes = domain.MediaReady, info.Size
	if err := m.store.ReplaceMedia(ctx, md); err != nil {
		return nil, err
	}
	return md, nil
}

// Link returns a URL for one media file: the CDN URL for public files, a
// 5-minute presigned GET for staff files (D-015).
func (m *MediaService) Link(ctx context.Context, id primitive.ObjectID) (string, error) {
	md, err := m.store.GetMedia(ctx, id)
	if err != nil {
		return "", err
	}
	if md.Visibility == domain.VisibilityPublic {
		return publicURL(m.aws().PublicMediaBaseURL, md.Key), nil
	}
	s3 := m.s3()
	if s3 == nil {
		return "", fmt.Errorf("media storage not configured")
	}
	ttl := m.links().PrivateLinkSeconds
	if ttl <= 0 {
		ttl = 300
	}
	return s3.PresignedGetObject(ctx, md.Bucket, md.Key, int64(ttl))
}

// publicURL is the CloudFront URL of a public object.
func publicURL(base, key string) string {
	if base == "" || key == "" {
		return ""
	}
	return base + "/" + key
}

func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if len(out) > maxFilenameLen {
		out = out[len(out)-maxFilenameLen:]
	}
	return out
}

// GC is the catalog.media_gc handler: unconfirmed uploads past the pending
// age, and old media no revision references, are deleted from storage and
// the collection.
func (m *MediaService) GC(ctx context.Context) error {
	cfg := m.cfg()
	now := m.now().UTC()
	pendingAge := time.Duration(max(cfg.MediaGcPendingHours, 1)) * time.Hour
	staleAge := time.Duration(max(cfg.MediaGcUnreferencedDays, 1)) * 24 * time.Hour
	candidates, err := m.store.MediaForGC(ctx, now.Add(-pendingAge), now.Add(-staleAge))
	if err != nil {
		return fmt.Errorf("media_gc: list: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}
	refs, err := m.store.ReferencedMedia(ctx)
	if err != nil {
		return fmt.Errorf("media_gc: references: %w", err)
	}
	s3 := m.s3()
	deleted := 0
	for _, md := range candidates {
		if md.Status != domain.MediaPending && refs[md.ID] {
			continue
		}
		if s3 != nil {
			if err := s3.DeleteFile(ctx, md.Bucket, md.Key); err != nil {
				log.Error("media_gc: delete object failed", "media", md.ID.Hex(), "error", err)
				continue
			}
		}
		if err := m.store.DeleteMedia(ctx, md.ID); err != nil && !errors.Is(err, errNotFound) {
			log.Error("media_gc: delete doc failed", "media", md.ID.Hex(), "error", err)
			continue
		}
		deleted++
	}
	log.Info("media_gc done", "candidates", len(candidates), "deleted", deleted)
	return nil
}

func resolveMediaGC(_ context.Context, occ cron.Occurrence) ([]cron.Unit, error) {
	return []cron.Unit{{UserID: systemUserID, Payload: struct{}{},
		IdempotencyKey: cron.UnitKey(JobMediaGC, occ.At.UTC().Format("20060102"), "sweep")}}, nil
}
