package company

import (
	"fmt"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Test seams.
var (
	listCompanyMemberships   = models.ListCompanyMemberships
	findMembershipByEmail    = models.FindCompanyMembershipByEmail
	findMembershipByID       = models.FindCompanyMembershipByID
	insertCompanyMembership  = models.InsertCompanyMembership
	rotateAcceptToken        = models.RotateCompanyMembershipAcceptToken
	archiveCompanyMembership = models.ArchiveCompanyMembership
	setCompanyMembershipRole = models.SetCompanyMembershipRole
	reserveCompanySeat       = models.ReserveCompanySeat
	releaseCompanySeatSlot   = models.ReleaseCompanySeatSlot
	transferCompanyOwnership = models.TransferCompanyOwnership
)

// HandleListMembers returns every membership row (invited/claimed/archived —
// the dashboard shows lifecycle). Any claimed member may read the roster.
func HandleListMembers(w http.ResponseWriter, r *http.Request) {
	active := middleware.GetActiveCompanyFromContext(r)
	memberships, err := listCompanyMemberships(r.Context(), active.Company.ID)
	if err != nil {
		middleware.GetLogger(r).Error("member list failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	owner := active.Company.OwnerUserID.Hex()
	out := make([]memberDTO, 0, len(memberships))
	for i := range memberships {
		out = append(out, toMemberDTO(&memberships[i], owner))
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"members": out})
}

type inviteMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteMemberResponse struct {
	Member    memberDTO `json:"member"`
	AcceptURL string    `json:"acceptUrl,omitempty"`
	Emailed   bool      `json:"emailed"`
}

// HandleInviteMember issues (or re-issues) an invite: seats were purchased
// first, the CAS'd SeatsUsed counter is THE gate (D18), and the invite email
// carries a one-time accept token (D3/D19). Re-inviting an archived email
// reactivates the same membership doc (D13).
func HandleInviteMember(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[inviteMemberRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyMembersInvite) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	role := req.Role
	if role == "" {
		role = models.CompanyRoleKeyUser
	}
	if !authz.IsCompanyRoleKey(role) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidCompanyRole)
		return
	}
	// Handing out user_admin is a roles write, not just an invite.
	if role == models.CompanyRoleKeyAdmin && !middleware.RequireCompanyPermission(r, authz.PermCompanyRolesWrite) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}

	email := models.NormalizeCompanySeatEmail(req.Email)
	if email == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	active := middleware.GetActiveCompanyFromContext(r)
	caller := middleware.GetUserFromContext(r)
	appCtx := config.GetAppContext(r)
	ttl := time.Duration(appCtx.Config.Values.Company.InviteTokenTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}

	raw, hash, err := mintToken()
	if err != nil {
		middleware.GetLogger(r).Error("invite token mint failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	expiresAt := time.Now().UTC().Add(ttl)

	found, existing, err := findMembershipByEmail(r.Context(), active.Company.ID, email)
	if err != nil {
		middleware.GetLogger(r).Error("invite lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}

	var membership *models.CompanyMembership
	switch {
	case found && existing.Status == models.CompanyMembershipStatusClaimed:
		middleware.SendJSONError(w, r, apperrors.ErrMemberAlreadyClaimed)
		return

	case found && existing.Status == models.CompanyMembershipStatusInvited:
		// Re-send: rotate the token; the seat is already occupied.
		if err := rotateAcceptToken(r.Context(), existing.ID, hash, expiresAt, caller.ID); err != nil {
			middleware.GetLogger(r).Error("invite token rotate failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
		membership = existing

	case found: // archived — D13 reclaim: takes a seat again.
		if err := seatGate(w, r, active.Company.ID); err != nil {
			return
		}
		if err := rotateAcceptToken(r.Context(), existing.ID, hash, expiresAt, caller.ID); err != nil {
			releaseSeatBestEffort(r, active.Company.ID)
			middleware.GetLogger(r).Error("invite reactivate failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
		membership = existing

	default:
		if err := seatGate(w, r, active.Company.ID); err != nil {
			return
		}
		fresh := &models.CompanyMembership{
			CompanyID:            active.Company.ID,
			Email:                email,
			Role:                 role,
			Status:               models.CompanyMembershipStatusInvited,
			InvitedByUserID:      caller.ID,
			AcceptTokenHash:      hash,
			AcceptTokenExpiresAt: &expiresAt,
		}
		if err := insertCompanyMembership(r.Context(), fresh); err != nil {
			// Insert failure rolls the counter back (D18).
			releaseSeatBestEffort(r, active.Company.ID)
			if models.IsDuplicateCompanyMembership(err) {
				middleware.SendJSONError(w, r, apperrors.ErrMemberAlreadyClaimed)
				return
			}
			middleware.GetLogger(r).Error("invite insert failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
		membership = fresh
	}

	acceptURL := renderLink(appCtx.Config.Values.Company.InviteURLTemplate, raw)
	emailed := false
	if acceptURL != "" && companyMailer != nil && companyMailer.Enabled() {
		body := fmt.Sprintf(
			"You've been invited to join %s on Indexly.\n\nAccept the invite (sign in with this email address):\n%s\n\nThis link expires in %d hours.",
			active.Company.Name, acceptURL, int(ttl.Hours()))
		if err := companyMailer.Send(r.Context(), email, "You've been invited to "+active.Company.Name, body); err != nil {
			middleware.GetLogger(r).Warn("invite email send failed — surface the accept link manually", "error", err, "email", email)
		} else {
			emailed = true
		}
	}

	// Re-read for the fresh lifecycle stamps.
	if refreshed, m, err := findMembershipByID(r.Context(), membership.ID); err == nil && refreshed {
		membership = m
	}
	middleware.GetLogger(r).Info("company invite issued", "company", active.Company.ID.Hex(), "emailed", emailed)
	middleware.SendJSONResponse(w, r, http.StatusOK, inviteMemberResponse{
		Member:    toMemberDTO(membership, active.Company.OwnerUserID.Hex()),
		AcceptURL: acceptURL,
		Emailed:   emailed,
	})
}

// seatGate reserves a seat under the company CAS (D18), translating a full
// company into the 409. Writes the response on failure.
func seatGate(w http.ResponseWriter, r *http.Request, companyID primitive.ObjectID) error {
	if err := reserveCompanySeat(r.Context(), companyID); err != nil {
		if err == models.ErrNoFreeCompanySeat {
			middleware.SendJSONError(w, r, apperrors.ErrNoFreeCompanySeat)
			return err
		}
		middleware.GetLogger(r).Error("seat reserve failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return err
	}
	return nil
}

func releaseSeatBestEffort(r *http.Request, companyID primitive.ObjectID) {
	if err := releaseCompanySeatSlot(r.Context(), companyID); err != nil {
		middleware.GetLogger(r).Error("seat rollback failed — reconcile SeatsUsed against the membership count", "error", err, "company", companyID.Hex())
	}
}

type membershipTargetRequest struct {
	MembershipID string `json:"membershipId"`
	Role         string `json:"role,omitempty"`
}

// resolveTargetMembership parses and loads the target, scoping it to the
// active company (a membershipId from another company must read as not
// found). Writes the response on failure.
func resolveTargetMembership(w http.ResponseWriter, r *http.Request, membershipID string) (*models.CompanyMembership, *middleware.ActiveCompany, bool) {
	active := middleware.GetActiveCompanyFromContext(r)
	oid, err := primitive.ObjectIDFromHex(membershipID)
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return nil, nil, false
	}
	found, m, err := findMembershipByID(r.Context(), oid)
	if err != nil {
		middleware.GetLogger(r).Error("membership load failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return nil, nil, false
	}
	if !found || m.CompanyID != active.Company.ID {
		middleware.SendJSONError(w, r, apperrors.ErrMembershipNotFound)
		return nil, nil, false
	}
	return m, active, true
}

// HandleRevokeMember archives a membership (never deletes, D13) and frees its
// seat. The owner's membership is never archivable (D21).
func HandleRevokeMember(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[membershipTargetRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyMembersRemove) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	m, active, ok := resolveTargetMembership(w, r, req.MembershipID)
	if !ok {
		return
	}
	if m.UserID != nil && *m.UserID == active.Company.OwnerUserID {
		middleware.SendJSONError(w, r, apperrors.ErrOwnerMembershipLocked)
		return
	}
	archived, err := archiveCompanyMembership(r.Context(), m.ID)
	if err != nil {
		middleware.GetLogger(r).Error("member archive failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	if archived {
		releaseSeatBestEffort(r, active.Company.ID)
		// A revoked CLAIMED member loses the company's team-subscription
		// projection — they fall back to their own sub or nothing. Best-effort:
		// the next company webhook event or admin recompute heals a failure.
		if m.Status == models.CompanyMembershipStatusClaimed && m.UserID != nil {
			if err := recomputeUserEntitlement(r.Context(), *m.UserID, models.AppIDIndexly); err != nil {
				middleware.GetLogger(r).Error("entitlement recompute after member revoke failed — next company webhook heals",
					"error", err, "user", m.UserID.Hex(), "company", active.Company.ID.Hex())
			}
		}
	}
	middleware.GetLogger(r).Info("company member archived", "company", active.Company.ID.Hex(), "membership", m.ID.Hex())
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"archived": archived})
}

// HandleSetMemberRole changes a member's company role. Role edits can never
// demote the owner below user_admin (D21).
func HandleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[membershipTargetRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	if !middleware.RequireCompanyPermission(r, authz.PermCompanyRolesWrite) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	if !authz.IsCompanyRoleKey(req.Role) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidCompanyRole)
		return
	}
	m, active, ok := resolveTargetMembership(w, r, req.MembershipID)
	if !ok {
		return
	}
	if m.UserID != nil && *m.UserID == active.Company.OwnerUserID && req.Role != models.CompanyRoleKeyAdmin {
		middleware.SendJSONError(w, r, apperrors.ErrOwnerMembershipLocked)
		return
	}
	if err := setCompanyMembershipRole(r.Context(), m.ID, req.Role); err != nil {
		middleware.GetLogger(r).Error("member role write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("company member role set", "company", active.Company.ID.Hex(), "membership", m.ID.Hex(), "role", req.Role)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"role": req.Role})
}

type transferOwnershipRequest struct {
	MembershipID string `json:"membershipId"`
}

// HandleTransferOwnership swaps OwnerUserID to another claimed user_admin
// member. OLD OWNER ONLY — no platform-admin path (D12/D17).
func HandleTransferOwnership(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[transferOwnershipRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	caller := middleware.GetUserFromContext(r)
	m, active, ok := resolveTargetMembership(w, r, req.MembershipID)
	if !ok {
		return
	}
	if active.Company.OwnerUserID != caller.ID {
		middleware.SendJSONError(w, r, apperrors.ErrNotCompanyOwner)
		return
	}
	if m.Status != models.CompanyMembershipStatusClaimed || m.Role != models.CompanyRoleKeyAdmin || m.UserID == nil || *m.UserID == caller.ID {
		middleware.SendJSONError(w, r, apperrors.ErrTransferTargetInvalid)
		return
	}
	if err := transferCompanyOwnership(r.Context(), active.Company.ID, caller.ID, *m.UserID); err != nil {
		middleware.GetLogger(r).Error("ownership transfer failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("company ownership transferred",
		"company", active.Company.ID.Hex(), "from", caller.ID.Hex(), "to", m.UserID.Hex())

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"ownerUserId": m.UserID.Hex(),
	})
}
