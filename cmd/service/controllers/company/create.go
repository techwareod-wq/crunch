package company

import (
	"net/http"
	"strings"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Test seams.
var createTeamCompany = models.CreateTeamCompany

type createCompanyRequest struct {
	Name       string `json:"name"`
	WebsiteURL string `json:"websiteUrl"`
}

// HandleCreateCompany mints a team company owned by the caller (tenancy plan
// P7 purchase flow, step 1). Both fields are required — the website URL is the
// learn source the plan purchase needs anyway (D14), so it's collected at
// creation. The company starts unbilled (seat gate shut); the FE switches to
// it (/switch — the only LastActiveCompanyID writer, D9).
func HandleCreateCompany(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[createCompanyRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	name := strings.TrimSpace(req.Name)
	website := strings.TrimSpace(req.WebsiteURL)
	if name == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	if website == "" {
		middleware.SendJSONError(w, r, apperrors.ErrCompanyWebsiteRequired)
		return
	}

	caller := middleware.GetUserFromContext(r)
	company, err := createTeamCompany(r.Context(), caller.ID, caller.Email, name, website)
	if err != nil {
		middleware.GetLogger(r).Error("team company create failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrCompanyCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("team company created", "company", company.ID.Hex(), "owner", caller.ID.Hex())
	middleware.SendJSONResponse(w, r, http.StatusOK, toCompanyDTO(company))
}
