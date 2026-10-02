package s3provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type s3Provider struct {
	client                 *s3.Client
	presignClient          *s3.PresignClient
	region                 string
	presignPutExpiry       time.Duration
	presignMultipartExpiry time.Duration
	maxConcurrency         int
}

func NewS3(region, accessKey, secretKey string, presignPutExpiry, presignMultipartExpiry time.Duration, maxConcurrency int) (interfaces.S3, error) {
	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(region),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg)
	return &s3Provider{
		client:                 client,
		presignClient:          s3.NewPresignClient(client),
		region:                 region,
		presignPutExpiry:       presignPutExpiry,
		presignMultipartExpiry: presignMultipartExpiry,
		maxConcurrency:         maxConcurrency,
	}, nil
}

func (p *s3Provider) UploadFile(ctx context.Context, key, localPath, bucket string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	_, err = p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   file,
	})
	return err
}

func (p *s3Provider) UploadFileUsingBytes(ctx context.Context, key, bucket string, data []byte) error {
	contentType := http.DetectContentType(data)

	_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	return err
}

// HeadObject reports an object's size and content type. A missing key is
// (found=false, nil), not an error.
func (p *s3Provider) HeadObject(ctx context.Context, bucket, key string) (dto.S3ObjectInfo, bool, error) {
	out, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return dto.S3ObjectInfo{}, false, nil
		}
		return dto.S3ObjectInfo{}, false, err
	}
	info := dto.S3ObjectInfo{}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		info.ContentType = *out.ContentType
	}
	return info, true, nil
}

func (p *s3Provider) abortMultipart(ctx context.Context, bucket, key string, uploadID *string) {
	_, err := p.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   &bucket,
		Key:      &key,
		UploadId: uploadID,
	})
	if err != nil {
		log.Error("failed to abort multipart upload", "bucket", bucket, "key", key, "error", err)
	}
}

func (p *s3Provider) DownloadFile(ctx context.Context, key, localPath, bucket string) error {
	result, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer result.Body.Close()

	outFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer outFile.Close()

	_, err = outFile.ReadFrom(result.Body)
	return err
}

func (p *s3Provider) ListItems(ctx context.Context, bucket, prefix string) ([]dto.S3Object, error) {
	output, err := p.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}

	objects := make([]dto.S3Object, 0, len(output.Contents))
	for _, obj := range output.Contents {
		s3Obj := dto.S3Object{}
		if obj.Key != nil {
			s3Obj.Key = *obj.Key
		}
		if obj.Size != nil {
			s3Obj.Size = *obj.Size
		}
		if obj.LastModified != nil {
			s3Obj.LastModified = *obj.LastModified
		}
		objects = append(objects, s3Obj)
	}
	return objects, nil
}

func (p *s3Provider) DeleteFile(ctx context.Context, bucket, key string) error {
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	return err
}

func (p *s3Provider) CopyObject(ctx context.Context, sourceBucket, sourceKey, destBucket, destKey string) error {
	copySource := fmt.Sprintf("%s/%s", sourceBucket, sourceKey)
	_, err := p.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &destBucket,
		Key:        &destKey,
		CopySource: &copySource,
	})
	return err
}

func (p *s3Provider) PresignedGetObject(ctx context.Context, bucket, key string, expirySeconds int64) (string, error) {
	req, err := p.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}, s3.WithPresignExpires(time.Duration(expirySeconds)*time.Second))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return req.URL, nil
}

func (p *s3Provider) PresignedGetObjectForPreview(ctx context.Context, bucket, key string, expirySeconds int64) (string, error) {
	disposition := "inline"
	input := &s3.GetObjectInput{
		Bucket:                     &bucket,
		Key:                        &key,
		ResponseContentDisposition: &disposition,
	}

	if ct := getContentTypeFromKey(key); ct != "" {
		input.ResponseContentType = &ct
	}

	req, err := p.presignClient.PresignGetObject(ctx, input,
		s3.WithPresignExpires(time.Duration(expirySeconds)*time.Second))
	if err != nil {
		return "", fmt.Errorf("presign get for preview: %w", err)
	}
	return req.URL, nil
}

func (p *s3Provider) GenerateSinglepartUploadPresignedURLs(
	ctx context.Context,
	bucket string,
	requests []dto.PresignedSinglepartPutRequest,
) ([]dto.PresignedSinglepartPutResponse, error) {
	results := make([]dto.PresignedSinglepartPutResponse, len(requests))
	errs := make([]error, len(requests))

	sem := make(chan struct{}, p.maxConcurrency)
	var wg sync.WaitGroup

	for i, r := range requests {
		wg.Add(1)
		go func(idx int, req dto.PresignedSinglepartPutRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ct := req.ContentType
			presigned, err := p.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
				Bucket:      &bucket,
				Key:         &req.Key,
				ContentType: &ct,
			}, s3.WithPresignExpires(p.presignPutExpiry))

			if err != nil {
				errs[idx] = fmt.Errorf("presign put %q: %w", req.Key, err)
				return
			}

			headers := make(http.Header)
			headers.Set("Content-Type", ct)
			for k, v := range presigned.SignedHeader {
				headers[k] = v
			}

			results[idx] = dto.PresignedSinglepartPutResponse{
				Key:     req.Key,
				URL:     presigned.URL,
				Headers: headers,
			}
		}(i, r)
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (p *s3Provider) GenerateMultipartUploadPresignedURLs(
	ctx context.Context,
	bucket string,
	req dto.PresignedMultipartPutRequest,
) (*dto.PresignedMultipartPutResponse, error) {
	ct := req.ContentType
	createOut, err := p.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      &bucket,
		Key:         &req.Key,
		ContentType: &ct,
	})
	if err != nil {
		return nil, fmt.Errorf("create multipart upload: %w", err)
	}

	parts := make([]dto.PresignedPart, req.PartCount)
	errs := make([]error, req.PartCount)

	sem := make(chan struct{}, p.maxConcurrency)
	var wg sync.WaitGroup

	for i := 0; i < req.PartCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			pn := int32(idx + 1)
			presigned, presignErr := p.presignClient.PresignUploadPart(ctx, &s3.UploadPartInput{
				Bucket:     &bucket,
				Key:        &req.Key,
				UploadId:   createOut.UploadId,
				PartNumber: &pn,
			}, s3.WithPresignExpires(p.presignMultipartExpiry))

			if presignErr != nil {
				errs[idx] = fmt.Errorf("presign part %d: %w", pn, presignErr)
				return
			}

			headers := make(http.Header)
			for k, v := range presigned.SignedHeader {
				headers[k] = v
			}

			parts[idx] = dto.PresignedPart{
				PartNumber: int(pn),
				URL:        presigned.URL,
				Headers:    headers,
			}
		}(i)
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			p.abortMultipart(ctx, bucket, req.Key, createOut.UploadId)
			return nil, err
		}
	}

	return &dto.PresignedMultipartPutResponse{
		UploadID: *createOut.UploadId,
		Key:      req.Key,
		Parts:    parts,
	}, nil
}

func (p *s3Provider) CompleteMultipartUpload(ctx context.Context, req dto.CompleteMultipartUploadRequest) error {
	completedParts := make([]types.CompletedPart, len(req.Parts))
	for i, part := range req.Parts {
		pn := int32(part.PartNumber)
		etag := part.ETag
		completedParts[i] = types.CompletedPart{
			PartNumber: &pn,
			ETag:       &etag,
		}
	}

	_, err := p.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   &req.Bucket,
		Key:      &req.Key,
		UploadId: &req.UploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}

// GetS3BucketAndFileNameFromLink parses an HTTPS virtual-hosted S3 URL
// (e.g. https://bucket.s3.region.amazonaws.com/key) into bucket and key.
func (p *s3Provider) GetS3BucketAndFileNameFromLink(link string) (bucket, key string, err error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", "", fmt.Errorf("parse URL: %w", err)
	}

	host := u.Hostname()
	idx := strings.Index(host, ".s3")
	if idx < 0 {
		return "", "", fmt.Errorf("not a virtual-hosted S3 URL: %q", link)
	}
	bucket = host[:idx]
	key = strings.TrimPrefix(u.Path, "/")
	return bucket, key, nil
}

// GetS3UrlFromBucketAndFileName constructs an HTTPS virtual-hosted S3 URL.
func (p *s3Provider) GetS3UrlFromBucketAndFileName(bucket, key string) string {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, p.region, key)
}

func (p *s3Provider) Region() string {
	return p.region
}

func getContentTypeFromKey(key string) string {
	ext := strings.ToLower(path.Ext(key))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".json":
		return "application/json"
	case ".html":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	default:
		return ""
	}
}

var _ interfaces.S3 = (*s3Provider)(nil)
