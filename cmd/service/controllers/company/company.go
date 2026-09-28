package company

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Test seams (admin-controller convention).
var (
	setCompanyProfile        = models.SetCompanyProfile
	claimCompanyDomain       = models.ClaimCompanyDomain
	findClaimedMembership    = models.FindClaimedMembership
	setUserLastActiveCompany = models.SetUserLastActiveCompany
	listClaimedMemberships   = models.ListClaimedMembershipsByUser
	findCompanyByID          = models.FindCompanyByID
)

// HandleCompany serves the active-company summary (GET) and profile patch
// (PATCH) on /v1/company.
func HandleCompany(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetCompany(w, r)
	case http.MethodPatch:
		handlePatchCompany(w, r)
	default:
		middleware.SendJSONError(w, r, apperrors.ErrMethodNotAllowed)
	}
}

type companySummaryResponse struct {
	Company     companyDTO `json:"company"`
	CompanyRole string     `json:"companyRole"`
	Permissions []string   `json:"permissions"`
}

func companySummary(active *middleware.ActiveCompany) companySummaryResponse {
	perms := make([]string, 0, len(active.Perms))
	for p := range active.Perms {
		perms = append(perms, string(p))
	}
	role := active.CompanyRole()
	return companySummaryResponse{
		Company:     toCompanyDTO(active.Company),
		CompanyRole: role,
		Permissions: perms,
	}
}

func handleGetCompany(w http.ResponseWriter, r *http.Request) {
	active := middleware.GetActiveCompanyFromContext(r)
	middleware.SendJSONResponse(w, r, http.StatusOK, companySummary(active))
}

type patchCompanyRequest struct {
	Name       string `json:"name"`
	WebsiteURL string `json:"websiteUrl"`
	Domain     string `json:"domain"`
}

func handlePatchCompany(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[patchCompanyRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	if !middleware.RequireCompanyPermission(r, authz.PermCompanySettingsWrite) {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyPermissionDenied)
		return
	}
	active := middleware.GetActiveCompanyFromContext(r)

	if req.Name != "" || req.WebsiteURL != "" {
		if err := setCompanyProfile(r.Context(), active.Company.ID, req.Name, req.WebsiteURL); err != nil {
			middleware.GetLogger(r).Error("company profile update failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
	}
	if req.Domain != "" {
		if err := claimCompanyDomain(r.Context(), active.Company.ID, req.Domain); err != nil {
			if models.IsDuplicateCompany(err) {
				middleware.SendJSONError(w, r, apperrors.ErrCompanyDomainTaken)
				return
			}
			middleware.GetLogger(r).Error("company domain claim failed", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
	}

	found, fresh, err := findCompanyByID(r.Context(), active.Company.ID)
	if err != nil || !found {
		middleware.GetLogger(r).Error("company re-read failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, toCompanyDTO(fresh))
}

// myCompanyDTO is one switcher entry: the company + the caller's role in it.
type myCompanyDTO struct {
	Company companyDTO `json:"company"`
	Role    string     `json:"role"`
	Active  bool       `json:"active"`
}

// HandleMyCompanies lists the caller's claimed memberships for the FE
// switcher ({user_id, status} index).
func HandleMyCompanies(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r)
	memberships, err := listClaimedMemberships(r.Context(), user.ID)
	if err != nil {
		middleware.GetLogger(r).Error("list memberships failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	out := make([]myCompanyDTO, 0, len(memberships))
	for i := range memberships {
		m := &memberships[i]
		found, company, err := findCompanyByID(r.Context(), m.CompanyID)
		if err != nil {
			middleware.GetLogger(r).Error("company load failed", "error", err, "company", m.CompanyID.Hex())
			middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
			return
		}
		if !found {
			continue
		}
		role := m.Role
		if company.OwnerUserID == user.ID {
			role = models.CompanyRoleKeyAdmin
		}
		out = append(out, myCompanyDTO{
			Company: toCompanyDTO(company),
			Role:    role,
			Active:  user.LastActiveCompanyID != nil && *user.LastActiveCompanyID == company.ID,
		})
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"companies": out})
}

type switchCompanyRequest struct {
	CompanyID string `json:"companyId"`
}

// HandleSwitchCompany updates user.LastActiveCompanyID (D9 — the switcher is
// the ONLY writer). The target must be one of the caller's own claimed
// memberships; the middleware re-validates on every later request.
func HandleSwitchCompany(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[switchCompanyRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	companyID, err := primitive.ObjectIDFromHex(req.CompanyID)
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	user := middleware.GetUserFromContext(r)

	found, _, err := findClaimedMembership(r.Context(), user.ID, companyID)
	if err != nil {
		middleware.GetLogger(r).Error("switch membership check failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrMembershipNotFound)
		return
	}
	if err := setUserLastActiveCompany(r.Context(), user.ID, companyID); err != nil {
		middleware.GetLogger(r).Error("switch write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"activeCompanyId": companyID.Hex()})
}
