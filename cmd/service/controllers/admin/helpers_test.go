package admin

import (
	"context"
	"net/http"
	"net/http/httptest"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func callerUser(role string) *models.User {
	return &models.User{ID: primitive.NewObjectID(), Email: "caller@x.com", Role: role}
}

// deserReq builds a POST request carrying caller + a deserialized DTO in
// context (what DeserializeJson + JWT auth set up in production).
func deserReq(caller *models.User, deserialized any) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/x", nil)
	ctx := context.WithValue(r.Context(), middleware.UserContextKey, caller)
	ctx = context.WithValue(ctx, middleware.DeserializerContextKey, deserialized)
	return r.WithContext(ctx)
}
