package admin

import (
	"net/http"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// listAdminActions is a seam for tests (mirrors findUserByID in target.go).
var listAdminActions = models.ListAdminActions

// whoamiResponse identifies the acting admin and their effective capabilities.
// It must never grow allowlist contents or other admins' identities: reaching
// this endpoint already proves membership, enumerating it would leak the
// roster. Permissions + Rank let the dashboard gate menu items by capability
// instead of assuming binary admin (RBAC plan §6).
type whoamiResponse struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	Rank        int      `json:"rank"`
}

// HandleAdminWhoami answers the dashboard's gate probe with the acting admin's
// identity + resolved permissions/rank. Non-admins never reach this handler
// (403 at the gate), which is the whole check.
func HandleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)
	perms := authz.EffectivePermissionKeys(user, appCtx.RolesCache, time.Now().UTC())
	if perms == nil {
		perms = []string{}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, whoamiResponse{
		ID:          user.ID.Hex(),
		Email:       user.Email,
		Name:        user.Name,
		Role:        user.Role,
		Permissions: perms,
		Rank:        authz.Rank(user, appCtx.RolesCache),
	})
}

// listAuditResponse pages the persisted audit trail, mirroring the users
// list shape.
type listAuditResponse struct {
	Actions []models.AdminAction `json:"actions"`
	Page    int                  `json:"page"`
	Limit   int                  `json:"limit"`
	Total   int64                `json:"total"`
}

// HandleAdminListAuditActions serves the audit trail newest-first
// (?page=&limit=, 1-based page) with optional ?targetUserId= and
// ?adminEmail= filters.
func HandleAdminListAuditActions(w http.ResponseWriter, r *http.Request) {
	pagination := config.GetAppContext(r).Config.Values.Admin.Pagination

	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		page = parsed
	}

	limit := pagination.AuditDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > pagination.AuditMax {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		limit = parsed
	}

	targetUserID := r.URL.Query().Get("targetUserId")
	if targetUserID != "" {
		if _, err := primitive.ObjectIDFromHex(targetUserID); err != nil {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
	}

	actions, total, err := listAdminActions(r.Context(), targetUserID, r.URL.Query().Get("adminEmail"), page, limit)
	if err != nil {
		middleware.GetLogger(r).Error("admin audit list failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, listAuditResponse{
		Actions: actions,
		Page:    page,
		Limit:   limit,
		Total:   total,
	})
}
