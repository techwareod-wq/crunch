package accountService

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// DeletionReport is what a deletion actually did — returned to the admin
// endpoint and logged. Counts are per-collection deleted-doc totals; a re-run
// of an already-completed cascade reports zeros everywhere.
type DeletionReport struct {
	SubscriptionsCanceled    int      `json:"subscriptionsCanceled"`
	SubscriptionsDeleted     int64    `json:"subscriptionsDeleted"`
	WebEntitiesDeleted       int64    `json:"webEntitiesDeleted"`
	WebEntityContextsDeleted int64    `json:"webEntityContextsDeleted"`
	KeywordsDeleted          int64    `json:"keywordsDeleted"`
	ScheduledArticlesDeleted int64    `json:"scheduledArticlesDeleted"`
	MasterContextsDeleted    int64    `json:"masterContextsDeleted"`
	AnalyticsRawDeleted      int64    `json:"analyticsRawDeleted"`
	AnalyticsFactsDeleted    int64    `json:"analyticsFactsDeleted"`
	S3ObjectsDeleted         int      `json:"s3ObjectsDeleted"`
	S3FailedKeys             []string `json:"s3FailedKeys,omitempty"` // best-effort — logged, never fatal
	PaddleCustomersDeleted   int64    `json:"paddleCustomersDeleted"` // delete-user only
	ClerkUserDeleted         bool     `json:"clerkUserDeleted"`       // delete-user only
	UserScrubbed             bool     `json:"userScrubbed"`           // delete-user only
}

// SubscriptionCanceler is the slice of PaymentService the cascade needs.
// Narrow by design so tests fake one method instead of the whole billing
// surface; the real PaymentService satisfies it.
type SubscriptionCanceler interface {
	CancelAllSubscriptionsImmediately(ctx context.Context, userID primitive.ObjectID) error
}

// ObjectStorage is the slice of the S3 provider the cascade needs (best-effort
// image cleanup); the real S3 provider satisfies it.
type ObjectStorage interface {
	DeleteFile(ctx context.Context, bucket, key string) error
}

// AccountService owns the admin-initiated destructive cascades. No Mongo
// transactions exist in this codebase, so both operations are ordered
// (abortable external calls first, children before parents) and every step is
// zero-match-OK — a mid-failure is repaired by simply re-running.
type AccountService interface {
	// DeleteWebEntityData deletes the user's whole SEO flow: cancels every
	// subscription (paid or trial) with cancel-now semantics, hard-deletes the
	// subscription docs (tombstoning them against webhook resurrection), and
	// hard-deletes webEntity/webEntityContext/keyword/scheduledArticle/
	// webEntityMasterContext plus (best-effort) their S3 images. The user
	// document is untouched beyond the entitlement projection recompute.
	DeleteWebEntityData(ctx context.Context, userID primitive.ObjectID, adminEmail string) (*DeletionReport, error)

	// DeleteUserAccount is DeleteWebEntityData plus account teardown: deletes
	// the paddle_customers mapping, deletes the user at Clerk (404 = already
	// gone), and finally soft-deletes the user doc with PII scrubbed.
	// transactions and adminActions are deliberately kept (finance + audit).
	DeleteUserAccount(ctx context.Context, user *models.User, adminEmail string) (*DeletionReport, error)
}
