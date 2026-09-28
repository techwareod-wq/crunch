package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func withFindUserSeam(t *testing.T, fn func(ctx context.Context, id string) (bool, *models.User, error)) {
	t.Helper()
	orig := findUserByID
	findUserByID = fn
	t.Cleanup(func() { findUserByID = orig })
}

// adminRequest builds a request with an authenticated admin in context, the
// way WithJWTAuthentication does in production.
func adminRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	adminUser := &models.User{ID: primitive.NewObjectID(), Email: "admin@x.com"}
	return r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, adminUser))
}

func TestResolveTargetUser_KnownTargetResolves(t *testing.T) {
	targetID := primitive.NewObjectID()
	withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		if id != targetID.Hex() {
			t.Fatalf("looked up id %q, want %q", id, targetID.Hex())
		}
		return true, &models.User{ID: targetID, Email: "target@x.com"}, nil
	})

	target, r, appErr := resolveTargetUser(adminRequest(), targetID.Hex())
	if appErr != nil {
		t.Fatalf("resolveTargetUser: unexpected error %v", appErr)
	}
	if target.ID != targetID {
		t.Errorf("target.ID = %s, want %s", target.ID.Hex(), targetID.Hex())
	}
	// The returned request must carry the enriched audit logger.
	if r.Context().Value(middleware.LoggerContextKey) == nil {
		t.Error("returned request has no audit logger in context")
	}
}

func TestResolveTargetUser_EmptyIDIs400(t *testing.T) {
	withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		t.Fatal("lookup must not run for an empty id")
		return false, nil, nil
	})

	_, _, appErr := resolveTargetUser(adminRequest(), "")
	if appErr == nil || appErr.Code != http.StatusBadRequest {
		t.Fatalf("empty id: err = %v, want 400", appErr)
	}
}

func TestResolveTargetUser_MalformedIDIs400(t *testing.T) {
	withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		t.Fatal("lookup must not run for a malformed id")
		return false, nil, nil
	})

	_, _, appErr := resolveTargetUser(adminRequest(), "not-a-hex-object-id")
	if appErr == nil || appErr.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: err = %v, want 400", appErr)
	}
}

func TestResolveTargetUser_UnknownTargetIs404(t *testing.T) {
	// Covers deactivated targets too: FindUserByID applies the active filter,
	// so a tombstoned user comes back not-found.
	withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return false, nil, nil
	})

	_, _, appErr := resolveTargetUser(adminRequest(), primitive.NewObjectID().Hex())
	if appErr == nil || appErr.Code != http.StatusNotFound {
		t.Fatalf("unknown target: err = %v, want 404", appErr)
	}
}

func TestResolveTargetUser_StoreErrorIs500Not404(t *testing.T) {
	withFindUserSeam(t, func(ctx context.Context, id string) (bool, *models.User, error) {
		return false, nil, errors.New("mongo timeout")
	})

	_, _, appErr := resolveTargetUser(adminRequest(), primitive.NewObjectID().Hex())
	if appErr == nil || appErr.Code != http.StatusInternalServerError {
		t.Fatalf("store error: err = %v, want 500", appErr)
	}
}
