package admin

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Staff invites (D-011, D-012): superuser-only (staff.invite). An invite is a
// Clerk sign-up invitation with the crunch role pre-attached; the role lands
// on first sign-in (internal/services/staffinvites). Removing staff is the
// existing POST /v1/admin/users/role back to `user`.

// Seams for tests (mirror listAdminActions in console.go).
var (
	insertStaffInvite                      = models.InsertStaffInvite
	setStaffInviteClerkID                  = models.SetStaffInviteClerkID
	deleteStaffInvite                      = models.DeleteStaffInvite
	findStaffInviteByID                    = models.FindStaffInviteByID
	markStaffInviteClosed                  = models.MarkStaffInviteClosed
	listStaffInvites                       = models.ListStaffInvites
	findUserByEmail                        = models.FindUserByEmail
	staffChangeLog        domain.ChangeLog = changelog.New()
)

// invitableRoles are the roles an invite may carry. superuser is never
// invitable (minted only via rolesmigrate).
var invitableRoles = map[string]bool{
	models.RoleKeyEditor:   true,
	models.RoleKeyApprover: true,
}

// inviteRoleMetadataKey is the Clerk public-metadata key carrying the role
// (informational for the admin frontend; crunch reads its own invite doc).
const inviteRoleMetadataKey = "whRole"

type adminStaffInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// HandleAdminStaffInvite serves POST /v1/admin/staff/invite {email, role}.
// The invite doc is inserted first (claiming the one-pending-per-email slot),
// then Clerk is called; a Clerk failure deletes the doc so a retry is clean.
func HandleAdminStaffInvite(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminStaffInviteRequest)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !ok || !invitableRoles[req.Role] || !validEmail(email) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidInvite)
		return
	}
	appCtx := config.GetAppContext(r)
	caller := middleware.GetUserFromContext(r)
	logger := middleware.GetLogger(r)

	// An existing account can't accept a sign-up invitation — its role is
	// set directly instead.
	exists, _, err := findUserByEmail(r.Context(), email)
	if err != nil {
		logger.Error("staff invite: user lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if exists {
		middleware.SendJSONError(w, r, apperrors.ErrStaffUserExists)
		return
	}

	inv := &models.StaffInvite{
		Email:           email,
		Role:            req.Role,
		InvitedBy:       strings.ToLower(caller.Email),
		InvitedByUserID: caller.ID.Hex(),
	}
	if err := insertStaffInvite(r.Context(), inv); err != nil {
		if errors.Is(err, models.ErrInvitePending) {
			middleware.SendJSONError(w, r, apperrors.ErrInvitePending)
			return
		}
		logger.Error("staff invite: insert failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	clerkID, err := appCtx.InternalServices.ClerkAccounts.CreateInvitation(r.Context(), email,
		map[string]any{inviteRoleMetadataKey: req.Role},
		appCtx.Config.Values.WarehouseHub.AdminBaseURL,
		appCtx.Config.Values.Admin.StaffInviteExpiryDays)
	if err != nil {
		logger.Error("staff invite: clerk invitation failed", "error", err)
		if derr := deleteStaffInvite(context.WithoutCancel(r.Context()), inv.ID); derr != nil {
			logger.Error("staff invite: rollback delete failed — revoke the invite by hand", "invite_id", inv.ID.Hex(), "error", derr)
		}
		middleware.SendJSONError(w, r, apperrors.ErrUpstreamUnavailable)
		return
	}
	inv.ClerkInvitationID = clerkID
	if err := setStaffInviteClerkID(r.Context(), inv.ID, clerkID); err != nil {
		// The invitation is out; only the stored id is missing (revoke then
		// needs a Clerk-dashboard step). Don't fail the request.
		logger.Error("staff invite: storing clerk invitation id failed", "invite_id", inv.ID.Hex(), "error", err)
	}

	staffChangeLog.Record(r.Context(), domain.ChangeEntry{
		Entity:   domain.EntityStaff,
		EntityID: inv.ID.Hex(),
		Action:   domain.ActionCreate,
		Actor:    domain.Actor{UserID: caller.ID.Hex(), Email: inv.InvitedBy},
		After:    inv,
		Meta:     map[string]any{"kind": "invite"},
	})
	logger.Info("staff invite sent", "invite_id", inv.ID.Hex(), "role", inv.Role)
	middleware.SendJSONResponse(w, r, http.StatusCreated, inv)
}

type listStaffInvitesResponse struct {
	Items []models.StaffInvite `json:"items"`
	Page  int                  `json:"page"`
	Limit int                  `json:"limit"`
	Total int64                `json:"total"`
}

// HandleAdminListStaffInvites serves GET /v1/admin/staff/invites
// (?status=&page=&limit=), newest first. Page bounds reuse the users list's.
func HandleAdminListStaffInvites(w http.ResponseWriter, r *http.Request) {
	pagination := config.GetAppContext(r).Config.Values.Admin.Pagination
	q := r.URL.Query()
	page, limit, ok := parsePageLimit(q.Get("page"), q.Get("limit"), pagination.UsersDefault, pagination.UsersMax)
	status := q.Get("status")
	switch status {
	case "", models.InviteStatusPending, models.InviteStatusAccepted, models.InviteStatusRevoked, models.InviteStatusExpired:
	default:
		ok = false
	}
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	items, total, err := listStaffInvites(r.Context(), status, page, limit)
	if err != nil {
		middleware.GetLogger(r).Error("staff invite list failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, listStaffInvitesResponse{Items: items, Page: page, Limit: limit, Total: total})
}

type adminRevokeStaffInviteRequest struct {
	ID string `json:"id"`
}

// HandleAdminRevokeStaffInvite serves POST /v1/admin/staff/invites/revoke
// {id}: revokes at Clerk (404 = already gone) then closes the invite.
func HandleAdminRevokeStaffInvite(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminRevokeStaffInviteRequest)
	id, err := primitive.ObjectIDFromHex(req.ID)
	if !ok || err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	found, inv, err := findStaffInviteByID(r.Context(), id)
	if err != nil {
		logger.Error("staff invite revoke: lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrNotFound)
		return
	}
	if inv.Status != models.InviteStatusPending {
		middleware.SendJSONError(w, r, apperrors.ErrInviteNotPending)
		return
	}
	if inv.ClerkInvitationID != "" {
		if err := appCtx.InternalServices.ClerkAccounts.RevokeInvitation(r.Context(), inv.ClerkInvitationID); err != nil {
			logger.Error("staff invite revoke: clerk revoke failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrUpstreamUnavailable)
			return
		}
	}
	if err := markStaffInviteClosed(r.Context(), id, models.InviteStatusRevoked); err != nil {
		if errors.Is(err, models.ErrInviteNotPending) {
			middleware.SendJSONError(w, r, apperrors.ErrInviteNotPending)
			return
		}
		logger.Error("staff invite revoke: close failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	caller := middleware.GetUserFromContext(r)
	after := *inv
	after.Status = models.InviteStatusRevoked
	staffChangeLog.Record(r.Context(), domain.ChangeEntry{
		Entity:   domain.EntityStaff,
		EntityID: inv.ID.Hex(),
		Action:   domain.ActionDelete,
		Actor:    domain.Actor{UserID: caller.ID.Hex(), Email: strings.ToLower(caller.Email)},
		Before:   inv,
		After:    &after,
		Meta:     map[string]any{"kind": "invite_revoke"},
	})
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"id": inv.ID.Hex(), "status": models.InviteStatusRevoked})
}

// validEmail accepts a bare address (no display name).
func validEmail(email string) bool {
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email
}
