package admin

import (
	"net/http"
	"strconv"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// listAdminActions is a seam for tests (mirrors findUserByID in target.go).
var listAdminActions = models.ListAdminActions

// whoamiResponse identifies the acting admin and what they may do; the admin
// UI gates menu items on Permissions. It never lists other admins.
type whoamiResponse struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

// HandleAdminWhoami answers the admin UI's gate probe. Non-admins never reach
// it (403 at the gate).
func HandleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	middleware.SendJSONResponse(w, r, http.StatusOK, whoamiResponse{
		ID:          user.ID.Hex(),
		Email:       user.Email,
		Name:        user.Name,
		Role:        user.Role,
		Permissions: authz.Effective(user),
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
