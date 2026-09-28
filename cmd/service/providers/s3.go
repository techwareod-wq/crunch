package providers

import (
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	s3provider "github.com/atharva-ng/crunch/internal/providers/impl/s3"
)

// InjectDefaultS3Provider wires the S3 provider. There is no dummy fallback:
// if S3 can't be initialised, startup must fail loudly rather than silently
// drop uploads and hand out bogus presigned URLs.
func InjectDefaultS3Provider(appCtx *config.AppContext) error {
	s3Values := appCtx.Config.Values.S3
	s3, err := s3provider.NewS3(
		appCtx.Config.AWS.Region,
		appCtx.Config.AWS.AccessKeyID,
		appCtx.Config.AWS.SecretAccessKey,
		time.Duration(s3Values.PresignPutExpirySeconds)*time.Second,
		time.Duration(s3Values.PresignMultipartExpirySeconds)*time.Second,
		s3Values.MaxConcurrency,
	)
	if err != nil {
		return fmt.Errorf("init S3 provider: %w", err)
	}
	appCtx.S3Provider = s3
	return nil
}
