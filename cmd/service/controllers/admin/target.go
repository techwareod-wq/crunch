// Package admin exposes the /v1/admin/* routes: thin mirrors of the user APIs
// that act on an explicitly named TARGET user instead of the JWT caller. Every
// handler calls the same service method as its user-facing twin, passing the
// target's ID — ownership filters keep working because they filter on whatever
// userId they are handed. Authorization is the DB-backed RBAC gate enforced by
// middleware.WithAdminAuthorization.
package admin

import (
	"context"
	"net/http"

	"go.mongodb.org/mongo-driver/bson/primitive"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// findUserByID is a seam for tests so target resolution runs without a live
// Mongo.
var findUserByID = models.FindUserByID

// resolveTargetUser validates the admin-supplied target user id and loads the
// full user doc: a clean 404 before any service call, the ObjectID the payment
// service needs, and the target's email for the audit trail. Unknown and
// deactivated targets look identical (FindUserByID applies the active filter).
//
// The returned request carries a logger enriched with the audit fields
// (admin_email, target_user_id, target_email) under LoggerContextKey, so every
// subsequent handler log line identifies both parties — use it in place of the
// original request.
func resolveTargetUser(r *http.Request, userId string) (*models.User, *http.Request, *apperrors.Error) {
	if userId == "" {
		return nil, r, apperrors.ErrInvalidRequestBody
	}
	if _, err := primitive.ObjectIDFromHex(userId); err != nil {
		return nil, r, apperrors.ErrInvalidRequestBody
	}

	found, target, err := findUserByID(r.Context(), userId)
	if err != nil {
		middleware.GetLogger(r).Error("admin target user lookup failed", "error", err, "target_user_id", userId)
		return nil, r, apperrors.ErrAdminCheckFailed
	}
	if !found {
		return nil, r, apperrors.ErrUserNotFound
	}

	adminUser := middleware.GetUserFromContext(r)
	logger := middleware.GetLogger(r).With(
		"admin_email", adminUser.Email,
		"target_user_id", target.ID.Hex(),
		"target_email", target.Email,
	)
	ctx := context.WithValue(r.Context(), middleware.LoggerContextKey, logger)
	return target, r.WithContext(ctx), nil
}
