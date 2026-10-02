package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

// S3 defines the contract for object storage operations.
type S3 interface {
	// Single-part uploads
	UploadFile(ctx context.Context, key, localPath, bucket string) error
	UploadFileUsingBytes(ctx context.Context, key, bucket string, data []byte) error

	// HeadObject returns an object's size and content type without reading
	// it — used to enforce upload limits on confirm. found is false when the
	// key does not exist.
	HeadObject(ctx context.Context, bucket, key string) (info dto.S3ObjectInfo, found bool, err error)

	// Download
	DownloadFile(ctx context.Context, key, localPath, bucket string) error

	// List
	ListItems(ctx context.Context, bucket, prefix string) ([]dto.S3Object, error)

	// Delete and copy
	DeleteFile(ctx context.Context, bucket, key string) error
	CopyObject(ctx context.Context, sourceBucket, sourceKey, destBucket, destKey string) error

	// Presigned GET
	PresignedGetObject(ctx context.Context, bucket, key string, expirySeconds int64) (string, error)
	PresignedGetObjectForPreview(ctx context.Context, bucket, key string, expirySeconds int64) (string, error)

	// Presigned PUT (client/browser uploads)
	GenerateSinglepartUploadPresignedURLs(ctx context.Context, bucket string, requests []dto.PresignedSinglepartPutRequest) ([]dto.PresignedSinglepartPutResponse, error)
	GenerateMultipartUploadPresignedURLs(ctx context.Context, bucket string, req dto.PresignedMultipartPutRequest) (*dto.PresignedMultipartPutResponse, error)
	CompleteMultipartUpload(ctx context.Context, req dto.CompleteMultipartUploadRequest) error

	// URL helpers
	GetS3BucketAndFileNameFromLink(link string) (bucket, key string, err error)
	GetS3UrlFromBucketAndFileName(bucket, key string) string

	// Config
	Region() string
}
