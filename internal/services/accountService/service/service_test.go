package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
)

// recorder collects every call in order across the fakes.
type recorder struct {
	calls []string
}

type fakeStore struct {
	rec *recorder
}

func (f *fakeStore) DeactivateAndScrubUserByID(ctx context.Context, id primitive.ObjectID) error {
	f.rec.calls = append(f.rec.calls, "scrub")
	return nil
}

type fakeClerk struct {
	rec *recorder
	err error
}

func (f *fakeClerk) DeleteUser(ctx context.Context, clerkID string) error {
	f.rec.calls = append(f.rec.calls, "clerk:"+clerkID)
	return f.err
}

// fakeCleaner deletes its docs on the first run and zero-matches afterwards,
// like a real zero-match-OK cleaner.
type fakeCleaner struct {
	rec  *recorder
	name string
	docs int64
	err  error
}

func (f *fakeCleaner) Name() string { return f.name }

func (f *fakeCleaner) DeleteUserData(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	f.rec.calls = append(f.rec.calls, "clean:"+f.name)
	if f.err != nil {
		return 0, f.err
	}
	n := f.docs
	f.docs = 0
	return n, nil
}

func newTestService(rec *recorder, clerkErr error, cleaners ...accountService.DataCleaner) accountService.AccountService {
	return NewService(&fakeStore{rec: rec}, &fakeClerk{rec: rec, err: clerkErr}, cleaners)
}

func testUser() *models.User {
	return &models.User{ID: primitive.NewObjectID(), ClerkID: "ck_1"}
}

// Feature data first, then Clerk, then the scrub last.
func TestDeleteUserAccount_Order(t *testing.T) {
	rec := &recorder{}
	svc := newTestService(rec, nil,
		&fakeCleaner{rec: rec, name: "a", docs: 3},
		&fakeCleaner{rec: rec, name: "b", docs: 1},
	)

	report, err := svc.DeleteUserAccount(context.Background(), testUser(), "admin@x.com")
	if err != nil {
		t.Fatalf("delete user failed: %v", err)
	}
	if got := strings.Join(rec.calls, " "); got != "clean:a clean:b clerk:ck_1 scrub" {
		t.Errorf("call order wrong: %s", got)
	}
	if !report.ClerkUserDeleted || !report.UserScrubbed || report.DataDeleted["a"] != 3 || report.DataDeleted["b"] != 1 {
		t.Errorf("unexpected report: %+v", report)
	}
}

// A cleaner failure aborts before Clerk and the scrub, leaving the user doc as
// the re-run anchor.
func TestDeleteUserAccount_CleanerFailureAbortsEarly(t *testing.T) {
	rec := &recorder{}
	svc := newTestService(rec, nil, &fakeCleaner{rec: rec, name: "a", err: errors.New("boom")})

	if _, err := svc.DeleteUserAccount(context.Background(), testUser(), "admin@x.com"); err == nil {
		t.Fatal("expected cleaner error")
	}
	if got := strings.Join(rec.calls, " "); got != "clean:a" {
		t.Errorf("nothing may run after a cleaner failure, got: %s", got)
	}
}

// A Clerk failure aborts BEFORE the scrub: the user doc stays intact and
// resolvable, so the admin can simply retry.
func TestDeleteUserAccount_ClerkFailureAbortsBeforeScrub(t *testing.T) {
	rec := &recorder{}
	svc := newTestService(rec, errors.New("clerk down"))

	if _, err := svc.DeleteUserAccount(context.Background(), testUser(), "admin@x.com"); err == nil {
		t.Fatal("expected clerk error")
	}
	for _, c := range rec.calls {
		if c == "scrub" {
			t.Error("scrub must not run after a clerk failure")
		}
	}
}

// Re-running a completed cascade must converge cleanly: cleaners zero-match,
// clerk 404s (fake nil), scrub converges — no error, zeroed counts.
func TestDeleteUserAccount_RerunConverges(t *testing.T) {
	rec := &recorder{}
	svc := newTestService(rec, nil, &fakeCleaner{rec: rec, name: "a", docs: 2})
	user := testUser()

	if _, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com"); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	report, err := svc.DeleteUserAccount(context.Background(), user, "admin@x.com")
	if err != nil {
		t.Fatalf("re-run failed: %v", err)
	}
	if report.DataDeleted["a"] != 0 {
		t.Errorf("re-run should observe an emptied database, got %+v", report)
	}
	if !report.UserScrubbed {
		t.Error("re-run must still converge the scrub")
	}
}
