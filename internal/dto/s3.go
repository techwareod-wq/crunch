package dto

import (
	"net/http"
	"time"
)

// S3ObjectInfo is the HeadObject result.
type S3ObjectInfo struct {
	Size        int64
	ContentType string
}

// --- Presigned single-part PUT ---

type PresignedSinglepartPutRequest struct {
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
}

type PresignedSinglepartPutResponse struct {
	Key     string      `json:"key"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers,omitempty"`
}

// --- Presigned multipart PUT ---

type PresignedMultipartPutRequest struct {
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
	PartCount   int    `json:"partCount"`
}

type PresignedPart struct {
	PartNumber int         `json:"partNumber"`
	URL        string      `json:"url"`
	Headers    http.Header `json:"headers,omitempty"`
}

type PresignedMultipartPutResponse struct {
	UploadID string          `json:"uploadId"`
	Key      string          `json:"key"`
	Parts    []PresignedPart `json:"parts"`
}

// --- Complete multipart ---

type UploadedPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

type CompleteMultipartUploadRequest struct {
	Bucket   string         `json:"bucket"`
	Key      string         `json:"key"`
	UploadID string         `json:"uploadId"`
	Parts    []UploadedPart `json:"parts"`
}

// --- List objects ---

type S3Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
}
