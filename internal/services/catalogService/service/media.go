package service

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
	idto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

const maxFilenameLen = 120

var (
	photoTypes = []string{"image/jpeg", "image/png", "image/webp"}
	docTypes   = []string{"application/pdf", "image/jpeg", "image/png", "image/webp"}
)

// mediaService handles uploads (D-015, D-062). Buckets come from config at
// call time.
type mediaService struct {
	store store.Store
	s3    func() interfaces.S3
	aws   func() config.AWSConfig
	links func() config.StorageValues
	cfg   func() config.CatalogValues
	now   func() time.Time
}

func (m *mediaService) limit(kind string) int64 {
	if kind == models.MediaPhoto {
		return m.cfg().MaxPhotoBytes
	}
	return m.cfg().MaxDocBytes
}

func (m *mediaService) bucket(visibility string) string {
	if visibility == models.VisibilityPublic {
		return m.aws().PublicBucket
	}
	return m.aws().PrivateBucket
}

// UploadURL pre-checks the upload and returns a presigned PUT. Photos are
// always public; agreements always staff (D-062).
func (m *mediaService) UploadURL(ctx context.Context, actor domain.Actor, req catalogService.UploadRequest) (catalogService.UploadResponse, error) {
	if _, err := m.store.GetWarehouse(ctx, req.WarehouseID); err != nil {
		return catalogService.UploadResponse{}, err
	}
	switch req.Kind {
	case models.MediaPhoto:
		req.DocType, req.Visibility = "", models.VisibilityPublic
		if !slices.Contains(photoTypes, req.ContentType) {
			return catalogService.UploadResponse{}, catalogService.Invalidf("photos must be one of %s", strings.Join(photoTypes, ", "))
		}
		n, err := m.store.CountPhotos(ctx, req.WarehouseID)
		if err != nil {
			return catalogService.UploadResponse{}, err
		}
		if max := m.cfg().MaxPhotos; max > 0 && n >= int64(max) {
			return catalogService.UploadResponse{}, catalogService.Invalidf("at most %d photos per warehouse", max)
		}
	case models.MediaDoc:
		switch req.DocType {
		case models.DocAgreement:
			req.Visibility = models.VisibilityStaff
		case models.DocFloorPlan, models.DocCertificate, models.DocOther:
		default:
			return catalogService.UploadResponse{}, catalogService.Invalidf("docType must be floor_plan, agreement, certificate or other")
		}
		if req.Visibility != models.VisibilityPublic && req.Visibility != models.VisibilityStaff {
			return catalogService.UploadResponse{}, catalogService.Invalidf("visibility must be public or staff")
		}
		if !slices.Contains(docTypes, req.ContentType) {
			return catalogService.UploadResponse{}, catalogService.Invalidf("docs must be one of %s", strings.Join(docTypes, ", "))
		}
	default:
		return catalogService.UploadResponse{}, catalogService.Invalidf("kind must be photo or doc")
	}
	if req.Bytes <= 0 || req.Bytes > m.limit(req.Kind) {
		return catalogService.UploadResponse{}, catalogService.Invalidf("file must be 1 byte – %d MB", m.limit(req.Kind)>>20)
	}
	name := cleanFilename(req.Filename)
	if name == "" {
		return catalogService.UploadResponse{}, catalogService.Invalidf("filename is required")
	}
	bucket := m.bucket(req.Visibility)
	s3 := m.s3()
	if bucket == "" || s3 == nil {
		return catalogService.UploadResponse{}, fmt.Errorf("media storage not configured")
	}
	id := primitive.NewObjectID()
	md := &models.WarehouseMedia{
		ID: id, WarehouseID: req.WarehouseID, Kind: req.Kind, DocType: req.DocType, Visibility: req.Visibility,
		Bucket: bucket, Key: fmt.Sprintf("wh/%s/%s/%s", req.WarehouseID.Hex(), id.Hex(), name),
		Filename: name, ContentType: req.ContentType, Bytes: req.Bytes, Status: models.MediaPending,
		UploadedBy: actor.Email, CreatedAt: m.now().UTC(),
	}
	urls, err := s3.GenerateSinglepartUploadPresignedURLs(ctx, bucket, []idto.PresignedSinglepartPutRequest{{Key: md.Key, ContentType: md.ContentType}})
	if err != nil || len(urls) != 1 {
		return catalogService.UploadResponse{}, fmt.Errorf("presign upload: %w", err)
	}
	if err := m.store.InsertMedia(ctx, md); err != nil {
		return catalogService.UploadResponse{}, err
	}
	return catalogService.UploadResponse{MediaID: id.Hex(), PutURL: urls[0].URL, Headers: urls[0].Headers}, nil
}

// Confirm checks the uploaded object (HeadObject) and marks it ready. An
// oversized or wrongly typed object is deleted.
func (m *mediaService) Confirm(ctx context.Context, id primitive.ObjectID) (*models.WarehouseMedia, error) {
	md, err := m.store.GetMedia(ctx, id)
	if err != nil {
		return nil, err
	}
	if md.Status == models.MediaReady {
		return md, nil
	}
	if md.Status != models.MediaPending {
		return nil, catalogService.Conflict(catalogService.CodeBadState, "media is %s", md.Status)
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
		return nil, catalogService.Conflict(catalogService.CodeNotUploaded, "the file hasn't reached storage yet — upload it, then confirm")
	}
	if info.Size <= 0 || info.Size > m.limit(md.Kind) || (info.ContentType != "" && info.ContentType != md.ContentType) {
		if err := s3.DeleteFile(ctx, md.Bucket, md.Key); err != nil {
			log.Error("catalog: delete rejected upload failed", "media", id.Hex(), "error", err)
		}
		md.Status = models.MediaOrphaned
		if err := m.store.ReplaceMedia(ctx, md); err != nil {
			return nil, err
		}
		return nil, catalogService.Invalidf("the uploaded file is too large or of the wrong type")
	}
	md.Status, md.Bytes = models.MediaReady, info.Size
	if err := m.store.ReplaceMedia(ctx, md); err != nil {
		return nil, err
	}
	return md, nil
}

// Link returns a URL for one media file: the CDN URL for public files, a
// 5-minute presigned GET for staff files (D-015).
func (m *mediaService) Link(ctx context.Context, id primitive.ObjectID) (string, error) {
	md, err := m.store.GetMedia(ctx, id)
	if err != nil {
		return "", err
	}
	if md.Visibility == models.VisibilityPublic {
		return domain.PublicMediaURL(m.aws().PublicMediaBaseURL, md.Key), nil
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
func (m *mediaService) GC(ctx context.Context) error {
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
		if md.Status != models.MediaPending && refs[md.ID] {
			continue
		}
		if s3 != nil {
			if err := s3.DeleteFile(ctx, md.Bucket, md.Key); err != nil {
				log.Error("media_gc: delete object failed", "media", md.ID.Hex(), "error", err)
				continue
			}
		}
		if err := m.store.DeleteMedia(ctx, md.ID); err != nil && !errors.Is(err, catalogService.ErrNotFound) {
			log.Error("media_gc: delete doc failed", "media", md.ID.Hex(), "error", err)
			continue
		}
		deleted++
	}
	log.Info("media_gc done", "candidates", len(candidates), "deleted", deleted)
	return nil
}
