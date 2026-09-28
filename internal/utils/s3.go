package utils

import (
	"fmt"
	"strings"
)

// ParseS3URI parses an s3://bucket/key URI into bucket and key components.
//
// Example:
//
//	s3://my-bucket/path/to/file.txt → bucket="my-bucket", key="path/to/file.txt"
func ParseS3URI(s3URI string) (bucket, key string, err error) {
	const prefix = "s3://"
	if !strings.HasPrefix(s3URI, prefix) {
		return "", "", fmt.Errorf("invalid S3 URI: must start with %s", prefix)
	}
	rest := strings.TrimPrefix(s3URI, prefix)
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return "", "", fmt.Errorf("invalid S3 URI: no key path after bucket in %q", s3URI)
	}
	return rest[:idx], rest[idx+1:], nil
}
