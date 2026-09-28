package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// adminPaginationAppCtx returns an AppContext carrying the default admin
// pagination bounds (matching values.yaml) so handlers that read
// config.GetAppContext(r).Config.Values.Admin.Pagination behave as they did
// when those bounds were hardcoded consts.
func adminPaginationAppCtx() *config.AppContext {
	appCtx := &config.AppContext{}
	appCtx.Config.Values.Admin.Pagination = config.AdminPaginationValues{
		UsersDefault: 20,
		UsersMax:     100,
		AuditDefault: 50,
		AuditMax:     200,
	}
	return appCtx
}

// serveWithAppCtx runs handler with the given request, injecting an AppContext
// that carries the default admin pagination bounds.
func serveWithAppCtx(rec http.ResponseWriter, handler http.HandlerFunc, r *http.Request) {
	adminPaginationAppCtx().Middleware()(handler).ServeHTTP(rec, r)
}

// withListAuditSeam swaps the audit list seam (same pattern as
// withFindUserSeam in target_test.go).
func withListAuditSeam(t *testing.T, fn func(ctx context.Context, targetUserID, adminEmail string, page, limit int) ([]models.AdminAction, int64, error)) {
	t.Helper()
	orig := listAdminActions
	listAdminActions = fn
	t.Cleanup(func() { listAdminActions = orig })
}

func TestHandleAdminWhoami_ReturnsActingAdminIdentity(t *testing.T) {
	id := primitive.NewObjectID()
	r := httptest.NewRequest(http.MethodGet, "/v1/admin/whoami", nil)
	r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, &models.User{
		ID:    id,
		Email: "admin@x.com",
		Name:  "Admin",
		Role:  "user",
	}))

	rec := httptest.NewRecorder()
	// whoami now resolves permissions/rank from the AppContext's roles cache;
	// inject one via the appctx middleware (a nil cache resolves nothing, which
	// is fine — this test only asserts identity fields).
	(&config.AppContext{}).Middleware()(http.HandlerFunc(HandleAdminWhoami)).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var envelope struct {
		Success bool           `json:"success"`
		Data    whoamiResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !envelope.Success || envelope.Data.Email != "admin@x.com" || envelope.Data.ID != id.Hex() {
		t.Errorf("whoami = %+v", envelope)
	}
}

func TestHandleAdminListAuditActions_RejectsBadParams(t *testing.T) {
	cases := map[string]string{
		"page zero":     "?page=0",
		"page non-int":  "?page=x",
		"limit zero":    "?limit=0",
		"limit too big": "?limit=201",
		"bad target id": "?targetUserId=not-hex",
	}
	for name, qs := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			serveWithAppCtx(rec, HandleAdminListAuditActions, httptest.NewRequest(http.MethodGet, "/v1/admin/audit"+qs, nil))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleAdminListAuditActions_PassesFiltersAndPages(t *testing.T) {
	target := primitive.NewObjectID().Hex()
	withListAuditSeam(t, func(ctx context.Context, targetUserID, adminEmail string, page, limit int) ([]models.AdminAction, int64, error) {
		if targetUserID != target || adminEmail != "admin@x.com" || page != 2 || limit != 10 {
			t.Errorf("seam args = (%q, %q, %d, %d)", targetUserID, adminEmail, page, limit)
		}
		return []models.AdminAction{{AdminEmail: adminEmail, Method: "POST", Path: "/v1/admin/seo-blog/retry", Status: 202}}, 11, nil
	})

	rec := httptest.NewRecorder()
	serveWithAppCtx(rec, HandleAdminListAuditActions, httptest.NewRequest(http.MethodGet,
		"/v1/admin/audit?page=2&limit=10&targetUserId="+target+"&adminEmail=admin@x.com", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data listAuditResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.Total != 11 || len(envelope.Data.Actions) != 1 || envelope.Data.Page != 2 {
		t.Errorf("response = %+v", envelope.Data)
	}
}
