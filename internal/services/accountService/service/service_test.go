package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// fakeStore records every call in order and (optionally) mutates its own
// state on deletes so re-runs observe an already-emptied database.
type fakeStore struct {
	calls  []string
	subs   []models.Subscription
	wecIDs []primitive.ObjectID
	s3Keys []string

	errOn string // method name that returns an error
}

func (f *fakeStore) fail(step string) error {
	if f.errOn == step {
		return errors.New(step + " boom")
	}
	return nil
}

func (f *fakeStore) FindSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) ([]models.Subscription, error) {
	f.calls = append(f.calls, "findSubs")
	return f.subs, f.fail("findSubs")
}
func (f *fakeStore) TombstoneSubscriptions(ctx context.Context, userID primitive.ObjectID, ids []string, by string) error {
	f.calls = append(f.calls, fmt.Sprintf("tombstone:%d", len(ids)))
	return f.fail("tombstone")
}
func (f *fakeStore) DeleteSubscriptionsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteSubs")
	n := int64(len(f.subs))
	f.subs = nil
	return n, f.fail("deleteSubs")
}
func (f *fakeStore) RecomputeUserEntitlement(ctx context.Context, userID primitive.ObjectID, appID string) error {
	f.calls = append(f.calls, "recompute:"+appID)
	return f.fail("recompute")
}
func (f *fakeStore) FindWebEntityContextIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	f.calls = append(f.calls, "findWECIDs")
	return f.wecIDs, f.fail("findWECIDs")
}
func (f *fakeStore) FindMasterContextImageS3KeysByUserID(ctx context.Context, userID primitive.ObjectID) ([]string, error) {
	f.calls = append(f.calls, "findS3Keys")
	return f.s3Keys, f.fail("findS3Keys")
}
func (f *fakeStore) DeleteWebEntityMasterContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteWEMC")
	n := int64(len(f.s3Keys))
	f.s3Keys = nil
	return n, f.fail("deleteWEMC")
}
func (f *fakeStore) DeleteScheduledArticlesByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteSA")
	return 0, f.fail("deleteSA")
}
func (f *fakeStore) DeleteKeywordsForWECs(ctx context.Context, wecIDs []primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, fmt.Sprintf("deleteKW:%d", len(wecIDs)))
	return int64(len(wecIDs)), f.fail("deleteKW")
}
func (f *fakeStore) DeleteWebEntityContextsByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteWEC")
	n := int64(len(f.wecIDs))
	f.wecIDs = nil
	return n, f.fail("deleteWEC")
}
func (f *fakeStore) FindWebEntityIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	f.calls = append(f.calls, "findWEIDs")
	return []primitive.ObjectID{primitive.NewObjectID()}, f.fail("findWEIDs")
}
func (f *fakeStore) DeleteAnalyticsRawByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteAnalyticsRaw")
	return 0, f.fail("deleteAnalyticsRaw")
}
func (f *fakeStore) DeleteAnalyticsFactsByWebEntityIDs(ctx context.Context, entityIDs []primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteAnalyticsFacts")
	return 0, f.fail("deleteAnalyticsFacts")
}
func (f *fakeStore) DeleteWebEntityByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteWE")
	return 1, f.fail("deleteWE")
}
func (f *fakeStore) DeletePaddleCustomersByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.calls = append(f.calls, "deleteCustomers")
	return 1, f.fail("deleteCustomers")
}
func (f *fakeStore) DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error {
	f.calls = append(f.calls, "scrub")
	return f.fail("scrub")
}

type fakeCanceler struct {
	store *fakeStore
	err   error
}

func (c *fakeCanceler) CancelAllSubscriptionsImmediately(ctx context.Context, userID primitive.ObjectID) error {
	c.store.calls = append(c.store.calls, "cancelAll")
	return c.err
}

type fakeS3 struct {
	store    *fakeStore
	failKeys map[string]bool
}

func (s *fakeS3) DeleteFile(ctx context.Context, bucket, key string) error {
	s.store.calls = append(s.store.calls, "s3:"+key)
	if s.failKeys[key] {
		return errors.New("s3 down")
	}
	return nil
}

type fakeClerk struct {
	store *fakeStore
	err   error
}

func (c *fakeClerk) DeleteUser(ctx context.Context, clerkID string) error {
	c.store.calls = append(c.store.calls, "clerk:"+clerkID)
	return c.err
}

func newTestService(st *fakeStore, cancelErr, clerkErr error, failKeys map[string]bool) *service {
	return &service{
		store:    st,
		payments: &fakeCanceler{store: st, err: cancelErr},
		s3:       &fakeS3{store: st, failKeys: failKeys},
		bucket:   "test-bucket",
		clerk:    &fakeClerk{store: st, err: clerkErr},
	}
}

func seededStore() *fakeStore {
	return &fakeStore{
		subs: []models.Subscription{
			{PaddleSubscriptionID: "sub_a", Status: models.SubStatusActive, AppID: models.AppIDIndexly},
			{PaddleSubscriptionID: "sub_old", Status: models.SubStatusCanceled, AppID: models.AppIDIndexly},
		},
		wecIDs: []primitive.ObjectID{primitive.NewObjectID()},
		s3Keys: []string{"img/a.png", "img/b.png"},
	}
}

// The ordering IS the safety contract: cancel (abortable, external) before
// tombstone, tombstone before doc delete, key/id reads before the deletes
// that destroy them, children before parents, parent last.
func TestDeleteWebEntityData_CallOrder(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, nil, nil, nil)

	report, err := svc.DeleteWebEntityData(context.Background(), primitive.NewObjectID(), "admin@x.com")
	if err != nil {
		t.Fatalf("cascade failed: %v", err)
	}

	want := "findSubs cancelAll tombstone:2 deleteSubs recompute:" + models.AppIDIndexly +
		" findWECIDs findS3Keys s3:img/a.png s3:img/b.png deleteWEMC deleteSA deleteKW:1 deleteWEC" +
		" findWEIDs deleteAnalyticsRaw deleteAnalyticsFacts deleteWE"
	if got := strings.Join(st.calls, " "); got != want {
		t.Errorf("call order:\n got  %s\n want %s", got, want)
	}
	if report.SubscriptionsCanceled != 1 {
		t.Errorf("subscriptions canceled = %d, want 1 (sub_old was already canceled)", report.SubscriptionsCanceled)
	}
	if report.SubscriptionsDeleted != 2 || report.S3ObjectsDeleted != 2 || report.KeywordsDeleted != 1 || report.WebEntitiesDeleted != 1 {
		t.Errorf("unexpected report: %+v", report)
	}
	if len(report.S3FailedKeys) != 0 {
		t.Errorf("no S3 failures expected, got %v", report.S3FailedKeys)
	}
}

// A Paddle failure must abort with NOTHING destroyed — that's why the cancel
// runs first.
func TestDeleteWebEntityData_PaddleFailureDestroysNothing(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, errors.New("paddle down"), nil, nil)

	if _, err := svc.DeleteWebEntityData(context.Background(), primitive.NewObjectID(), "admin@x.com"); err == nil {
		t.Fatal("expected error from paddle failure")
	}
	for _, c := range st.calls {
		if strings.HasPrefix(c, "delete") || strings.HasPrefix(c, "tombstone") || strings.HasPrefix(c, "s3:") {
			t.Errorf("destructive call %q happened after paddle failure", c)
		}
	}
}

// S3 is best-effort: a failed object is reported, never fatal, and the Mongo
// cascade still completes.
func TestDeleteWebEntityData_S3FailureIsNonFatal(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, nil, nil, map[string]bool{"img/a.png": true})

	report, err := svc.DeleteWebEntityData(context.Background(), primitive.NewObjectID(), "admin@x.com")
	if err != nil {
		t.Fatalf("cascade must not fail on S3 errors: %v", err)
	}
	if len(report.S3FailedKeys) != 1 || report.S3FailedKeys[0] != "img/a.png" {
		t.Errorf("failed keys = %v, want [img/a.png]", report.S3FailedKeys)
	}
	if report.S3ObjectsDeleted != 1 {
		t.Errorf("s3 deleted = %d, want 1", report.S3ObjectsDeleted)
	}
	if st.calls[len(st.calls)-1] != "deleteWE" {
		t.Errorf("cascade did not run to completion: %v", st.calls)
	}
}

// Full account deletion appends: paddle customer mapping → Clerk → scrub LAST.
func TestDeleteUserAccount_Order(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, nil, nil, nil)
	user := &models.User{ID: primitive.NewObjectID(), ClerkID: "ck_1"}

	report, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com")
	if err != nil {
		t.Fatalf("delete user failed: %v", err)
	}
	got := strings.Join(st.calls, " ")
	if !strings.HasSuffix(got, "deleteWE deleteCustomers clerk:ck_1 scrub") {
		t.Errorf("teardown suffix wrong: %s", got)
	}
	if !report.ClerkUserDeleted || !report.UserScrubbed || report.PaddleCustomersDeleted != 1 {
		t.Errorf("unexpected report: %+v", report)
	}
}

// A Clerk failure aborts BEFORE the scrub: the user doc stays intact and
// resolvable, so the admin can simply retry.
func TestDeleteUserAccount_ClerkFailureAbortsBeforeScrub(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, nil, errors.New("clerk down"), nil)
	user := &models.User{ID: primitive.NewObjectID(), ClerkID: "ck_1"}

	if _, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com"); err == nil {
		t.Fatal("expected clerk error")
	}
	for _, c := range st.calls {
		if c == "scrub" {
			t.Error("scrub must not run after a clerk failure")
		}
	}
}

// Re-running a completed cascade must converge cleanly: all deletes zero-match,
// clerk 404s (fake nil), scrub converges — no error, zeroed counts.
func TestDeleteUserAccount_RerunConverges(t *testing.T) {
	st := seededStore()
	svc := newTestService(st, nil, nil, nil)
	user := &models.User{ID: primitive.NewObjectID(), ClerkID: "ck_1"}

	if _, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com"); err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	st.calls = nil
	report, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com")
	if err != nil {
		t.Fatalf("re-run failed: %v", err)
	}
	if report.SubscriptionsCanceled != 0 || report.SubscriptionsDeleted != 0 || report.S3ObjectsDeleted != 0 || report.KeywordsDeleted != 0 {
		t.Errorf("re-run should observe an emptied database, got %+v", report)
	}
	if !report.UserScrubbed {
		t.Error("re-run must still converge the scrub")
	}
}
