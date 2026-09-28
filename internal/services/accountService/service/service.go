package service

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	accountstore "github.com/atharva-ng/crunch/internal/services/accountService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type service struct {
	store    accountstore.Store
	payments accountService.SubscriptionCanceler
	s3       accountService.ObjectStorage
	bucket   string
	clerk    interfaces.ClerkAccounts
}

func NewService(
	st accountstore.Store,
	payments accountService.SubscriptionCanceler,
	s3 accountService.ObjectStorage,
	bucket string,
	clerk interfaces.ClerkAccounts,
) accountService.AccountService {
	return &service{store: st, payments: payments, s3: s3, bucket: bucket, clerk: clerk}
}

// DeleteWebEntityData runs the SEO-flow cascade. Step order is the safety
// argument (no transactions exist — ordering and idempotency are the only
// tools):
//
//  1. read subscriptions (ids + apps) BEFORE any mutation
//  2. cancel everything at Paddle — external and abortable FIRST: a failure
//     here aborts with nothing destroyed
//  3. tombstone the subscription ids BEFORE deleting the docs, closing the
//     webhook-resurrection window
//  4. hard-delete subscription docs
//  5. recompute entitlement projections (subscriptions-write invariant)
//  6. capture WEC ids and S3 keys BEFORE deleting the docs that hold them
//  7. best-effort S3 deletes — failures logged + reported, never fatal
//  8. delete children before parents: master contexts → scheduled articles →
//     keywords (by captured WEC ids) → WECs → web entity LAST, so a crash at
//     any point leaves the parent as the re-run anchor
func (s *service) DeleteWebEntityData(ctx context.Context, userID primitive.ObjectID, adminEmail string) (*accountService.DeletionReport, error) {
	report := &accountService.DeletionReport{}

	subs, err := s.store.FindSubscriptionsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}

	if err := s.payments.CancelAllSubscriptionsImmediately(ctx, userID); err != nil {
		return nil, fmt.Errorf("cancel subscriptions: %w", err)
	}

	subIDs := make([]string, 0, len(subs))
	for _, sub := range subs {
		subIDs = append(subIDs, sub.PaddleSubscriptionID)
		if sub.Status != models.SubStatusCanceled {
			report.SubscriptionsCanceled++
		}
	}
	if err := s.store.TombstoneSubscriptions(ctx, userID, subIDs, adminEmail); err != nil {
		return nil, fmt.Errorf("tombstone subscriptions: %w", err)
	}

	if report.SubscriptionsDeleted, err = s.store.DeleteSubscriptionsByUserID(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete subscriptions: %w", err)
	}

	// Recompute every app the user held a subscription for; always include the
	// default app so a re-run (subs already gone) still converges the
	// projection. Sorted for deterministic ordering.
	apps := map[string]struct{}{models.AppIDIndexly: {}}
	for _, sub := range subs {
		if sub.AppID != "" {
			apps[sub.AppID] = struct{}{}
		}
	}
	appIDs := make([]string, 0, len(apps))
	for app := range apps {
		appIDs = append(appIDs, app)
	}
	sort.Strings(appIDs)
	for _, app := range appIDs {
		if err := s.store.RecomputeUserEntitlement(ctx, userID, app); err != nil {
			return nil, fmt.Errorf("recompute entitlement for %s: %w", app, err)
		}
	}

	wecIDs, err := s.store.FindWebEntityContextIDsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("read web entity context ids: %w", err)
	}
	s3Keys, err := s.store.FindMasterContextImageS3KeysByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("read master context s3 keys: %w", err)
	}

	for _, key := range s3Keys {
		if err := s.s3.DeleteFile(ctx, s.bucket, key); err != nil {
			log.Error("s3 delete failed during deletion cascade — continuing",
				"error", err, "user_id", userID.Hex(), "s3_key", key)
			report.S3FailedKeys = append(report.S3FailedKeys, key)
			continue
		}
		report.S3ObjectsDeleted++
	}

	if report.MasterContextsDeleted, err = s.store.DeleteWebEntityMasterContextsByUserID(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete master contexts: %w", err)
	}
	if report.ScheduledArticlesDeleted, err = s.store.DeleteScheduledArticlesByUserID(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete scheduled articles: %w", err)
	}
	if report.KeywordsDeleted, err = s.store.DeleteKeywordsForWECs(ctx, wecIDs); err != nil {
		return nil, fmt.Errorf("delete keywords: %w", err)
	}
	if report.WebEntityContextsDeleted, err = s.store.DeleteWebEntityContextsByUserID(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete web entity contexts: %w", err)
	}
	// Analytics rows are keyed by web_entity_id, so the ids are resolved
	// before the entity docs go (children before parents, like everything
	// else here). Integration state itself dies with the WebEntity doc;
	// analyticsReplays docs are global bookkeeping and deliberately kept.
	entityIDs, err := s.store.FindWebEntityIDsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("read web entity ids: %w", err)
	}
	if report.AnalyticsRawDeleted, err = s.store.DeleteAnalyticsRawByWebEntityIDs(ctx, entityIDs); err != nil {
		return nil, fmt.Errorf("delete analytics raw: %w", err)
	}
	if report.AnalyticsFactsDeleted, err = s.store.DeleteAnalyticsFactsByWebEntityIDs(ctx, entityIDs); err != nil {
		return nil, fmt.Errorf("delete analytics facts: %w", err)
	}
	if report.WebEntitiesDeleted, err = s.store.DeleteWebEntityByUserID(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete web entity: %w", err)
	}

	log.Info("web entity data deleted",
		"user_id", userID.Hex(), "admin", adminEmail,
		"subscriptions_deleted", report.SubscriptionsDeleted,
		"web_entities_deleted", report.WebEntitiesDeleted,
		"keywords_deleted", report.KeywordsDeleted,
		"scheduled_articles_deleted", report.ScheduledArticlesDeleted,
		"analytics_raw_deleted", report.AnalyticsRawDeleted,
		"analytics_facts_deleted", report.AnalyticsFactsDeleted,
		"s3_objects_deleted", report.S3ObjectsDeleted,
		"s3_failed_keys", len(report.S3FailedKeys))
	return report, nil
}

// DeleteUserAccount runs the full cascade, then account teardown. Clerk runs
// before the scrub so every abortable external call precedes the final local
// writes: a Clerk failure leaves the user doc intact and the endpoint cleanly
// retryable. Once Clerk succeeds its user.deleted webhook may fire
// concurrently — the webhook handler's tombstone + cancel are idempotent
// no-ops against this cascade, and the scrub's writes absorb either ordering.
func (s *service) DeleteUserAccount(ctx context.Context, user *models.User, adminEmail string) (*accountService.DeletionReport, error) {
	report, err := s.DeleteWebEntityData(ctx, user.ID, adminEmail)
	if err != nil {
		return nil, err
	}

	if report.PaddleCustomersDeleted, err = s.store.DeletePaddleCustomersByUserID(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("delete paddle customers: %w", err)
	}

	if user.ClerkID != "" {
		if err := s.clerk.DeleteUser(ctx, user.ClerkID); err != nil {
			return nil, err
		}
	}
	report.ClerkUserDeleted = true

	if err := s.store.DeactivateAndScrubUserByID(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("scrub user: %w", err)
	}
	report.UserScrubbed = true

	log.Info("user account deleted",
		"user_id", user.ID.Hex(), "admin", adminEmail,
		"clerk_user_deleted", report.ClerkUserDeleted,
		"paddle_customers_deleted", report.PaddleCustomersDeleted)
	return report, nil
}
