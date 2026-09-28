package company

import (
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Test seams.
var (
	listPendingInvitesByEmail = models.ListPendingInvitesByEmail
	findMembershipByToken     = models.FindCompanyMembershipByAcceptTokenHash
	claimCompanyMembership    = models.ClaimCompanyMembership
	recomputeUserEntitlement  = models.RecomputeUserEntitlement
)

type pendingInviteDTO struct {
	MembershipID string `json:"membershipId"`
	CompanyID    string `json:"companyId"`
	CompanyName  string `json:"companyName"`
	Role         string `json:"role"`
	InvitedAt    string `json:"invitedAt,omitempty"`
}

// HandlePendingInvites surfaces every pending invite for the caller's email —
// the plural {email, status} Find (§3.2: a user invited to N companies must
// see all N). Feeds the dashboard nudge and the accept screen; the claim
// itself is ONLY the accept endpoint.
func HandlePendingInvites(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	if user.Email == "" {
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"invites": []pendingInviteDTO{}})
		return
	}
	invites, err := listPendingInvitesByEmail(r.Context(), user.Email)
	if err != nil {
		middleware.GetLogger(r).Error("pending invites lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	out := make([]pendingInviteDTO, 0, len(invites))
	for i := range invites {
		m := &invites[i]
		dto := pendingInviteDTO{
			MembershipID: m.ID.Hex(),
			CompanyID:    m.CompanyID.Hex(),
			Role:         m.Role,
		}
		if !m.InvitedAt.IsZero() {
			dto.InvitedAt = m.InvitedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		if found, company, err := findCompanyByID(r.Context(), m.CompanyID); err == nil && found {
			dto.CompanyName = company.Name
		}
		out = append(out, dto)
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"invites": out})
}

type acceptInviteRequest struct {
	Token string `json:"token"`
}

// HandleAcceptInvite is THE claim (D3/D19): a valid unexpired accept token +
// an authenticated Clerk session + a verified email matching the invite
// (normalized on both sides) — all three, or no membership flips. The CAS
// write flips each membership exactly once under racing accepts.
func HandleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[acceptInviteRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	if req.Token == "" {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyInviteInvalid)
		return
	}
	user := middleware.GetUserFromContext(r)
	if user.Email == "" {
		middleware.SendJSONError(w, r, apperrors.ErrUserEmailRequired)
		return
	}

	hash := hashToken(req.Token)
	found, m, err := findMembershipByToken(r.Context(), hash)
	if err != nil {
		middleware.GetLogger(r).Error("invite token lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	if !found {
		// Unknown, already-claimed, revoked — one uniform answer.
		middleware.SendJSONError(w, r, apperrors.ErrCompanyInviteInvalid)
		return
	}
	if models.NormalizeCompanySeatEmail(user.Email) != m.Email {
		middleware.SendJSONError(w, r, apperrors.ErrInviteEmailMismatch)
		return
	}

	claimed, err := claimCompanyMembership(r.Context(), m.ID, user.ID, hash)
	if err != nil {
		middleware.GetLogger(r).Error("invite claim failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	if !claimed {
		// Expired token or a racing accept won — uniform answer again.
		middleware.SendJSONError(w, r, apperrors.ErrCompanyInviteInvalid)
		return
	}

	// A claimed membership may project the company's team subscription onto
	// this user (seat billing). Best-effort: a failure here means the member
	// sees their features on the next company webhook event or admin
	// recompute, not never.
	if err := recomputeUserEntitlement(r.Context(), user.ID, models.AppIDIndexly); err != nil {
		middleware.GetLogger(r).Error("entitlement recompute after invite claim failed — next company webhook heals",
			"error", err, "user", user.ID.Hex(), "company", m.CompanyID.Hex())
	}

	cFound, company, err := findCompanyByID(r.Context(), m.CompanyID)
	if err != nil || !cFound {
		middleware.GetLogger(r).Error("company load after claim failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("company invite accepted", "company", company.ID.Hex(), "membership", m.ID.Hex())
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"company":      toCompanyDTO(company),
		"membershipId": m.ID.Hex(),
		"role":         m.Role,
	})
}
