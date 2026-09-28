package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
)

// Handler-level guards for the delete-user endpoint. Every DB/service touch
// goes through the package-var seams in deletion.go / target.go, so these run
// in-memory with no Mongo.

// --- seams ------------------------------------------------------------------

func withFindUserByIDSeam(t *testing.T, fn func(ctx context.Context, id string) (bool, *models.User, error)) {
	t.Helper()
	orig := findUserByID
	findUserByID = fn
	t.Cleanup(func() { findUserByID = orig })
}

func withFindUserIncludingDeactivatedSeam(t *testing.T, fn func(ctx context.Context, id string) (bool, *models.User, error)) {
	t.Helper()
	orig := findUserByIDIncludingDeactivated
	findUserByIDIncludingDeactivated = fn
	t.Cleanup(func() { findUserByIDIncludingDeactivated = orig })
}

func withDeleteUserAccountSeam(t *testing.T, fn func(r *http.Request, target *models.User, adminEmail string) (*accountService.DeletionReport, error)) {
	t.Helper()
	orig := deleteUserAccountFn
	deleteUserAccountFn = fn
	t.Cleanup(func() { deleteUserAccountFn = orig })
}

// deleteNeverCalled fails the test if the deletion service seam fires.
func deleteNeverCalled(t *testing.T) {
	t.Helper()
	withDeleteUserAccountSeam(t, func(r *http.Request, target *models.User, adminEmail string) (*accountService.DeletionReport, error) {
		t.Error("DeleteUserAccount must not be called")
		return nil, nil
	})
}

func serveDeletion(handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

// --- delete user guards ------------------------------------------------------

func TestDeleteUser_InvalidBody(t *testing.T) {
	deleteNeverCalled(t)
	caller := callerUser(models.RoleKeySuperuser)

	for _, userID := range []string{"", "not-a-hex-id"} {
		rec := serveDeletion(HandleAdminDeleteUser, deserReq(caller, adminDeleteUserRequest{UserID: userID}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("userId %q: status = %d, want 400", userID, rec.Code)
		}
	}
}

func TestDeleteUser_UnknownTarget(t *testing.T) {
	deleteNeverCalled(t)
	withFindUserIncludingDeactivatedSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return false, nil, nil
	})

	rec := serveDeletion(HandleAdminDeleteUser, deserReq(callerUser(models.RoleKeySuperuser),
		adminDeleteUserRequest{UserID: primitive.NewObjectID().Hex()}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteUser_SelfDeletionRefused(t *testing.T) {
	deleteNeverCalled(t)
	caller := callerUser(models.RoleKeySuperuser)
	withFindUserIncludingDeactivatedSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return true, caller, nil // target IS the caller
	})

	rec := serveDeletion(HandleAdminDeleteUser, deserReq(caller, adminDeleteUserRequest{UserID: caller.ID.Hex()}))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (self-deletion)", rec.Code)
	}
}

func TestDeleteUser_SuperuserTargetRefused(t *testing.T) {
	deleteNeverCalled(t)
	target := &models.User{ID: primitive.NewObjectID(), Email: "root@x.com", Role: models.RoleKeySuperuser}
	withFindUserIncludingDeactivatedSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return true, target, nil
	})

	rec := serveDeletion(HandleAdminDeleteUser, deserReq(callerUser(models.RoleKeySuperuser),
		adminDeleteUserRequest{UserID: target.ID.Hex()}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (superuser undeletable)", rec.Code)
	}
}

// A target the Clerk webhook already tombstoned mid-cascade must still resolve
// so the admin can re-run the deletion to completion.
func TestDeleteUser_TombstonedTargetStillResolvable(t *testing.T) {
	deactivatedAt := time.Now()
	target := &models.User{ID: primitive.NewObjectID(), Email: "gone@x.com", Role: models.RoleKeyUser, DeactivatedAt: &deactivatedAt}
	withFindUserIncludingDeactivatedSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return true, target, nil
	})

	called := false
	withDeleteUserAccountSeam(t, func(r *http.Request, got *models.User, adminEmail string) (*accountService.DeletionReport, error) {
		called = true
		if got.ID != target.ID {
			t.Errorf("service got target %s, want %s", got.ID.Hex(), target.ID.Hex())
		}
		return &accountService.DeletionReport{UserScrubbed: true}, nil
	})

	rec := serveDeletion(HandleAdminDeleteUser, deserReq(callerUser(models.RoleKeySuperuser),
		adminDeleteUserRequest{UserID: target.ID.Hex()}))
	if rec.Code != http.StatusOK || !called {
		t.Errorf("status = %d (called=%v), want 200 with service called", rec.Code, called)
	}
}

func TestDeleteUser_ServiceErrorIs500(t *testing.T) {
	target := &models.User{ID: primitive.NewObjectID(), Role: models.RoleKeyUser}
	withFindUserIncludingDeactivatedSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return true, target, nil
	})
	withDeleteUserAccountSeam(t, func(r *http.Request, got *models.User, adminEmail string) (*accountService.DeletionReport, error) {
		return nil, errors.New("clerk down")
	})

	rec := serveDeletion(HandleAdminDeleteUser, deserReq(callerUser(models.RoleKeySuperuser),
		adminDeleteUserRequest{UserID: target.ID.Hex()}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
