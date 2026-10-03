package dto

import (
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/models"
)

type UserResponse struct {
	ID      string `json:"id"`
	ClerkID string `json:"clerk_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	// Features are the visitor features the user may use (staff: all).
	Features []string `json:"features"`
	// Visitor profile (D-100).
	Phone            string     `json:"phone"`
	PhoneE164        string     `json:"phoneE164"`
	Company          string     `json:"company"`
	ProfileUpdatedAt *time.Time `json:"profileUpdatedAt,omitempty"`
}

func (u *UserResponse) FromDbModel(m *models.User) error {
	u.ID = m.ID.Hex()
	u.ClerkID = m.ClerkID
	u.Email = m.Email
	u.Name = m.Name
	u.Role = m.Role
	u.Features = authz.EffectiveFeatures(m)
	u.Phone = m.Phone
	u.PhoneE164 = m.PhoneE164
	u.Company = m.Company
	u.ProfileUpdatedAt = m.ProfileUpdatedAt
	return nil
}

// UpdateProfileRequest is the POST /v1/me/profile body. Both fields replace
// the stored values; an empty phone clears it.
type UpdateProfileRequest struct {
	Phone   string `json:"phone"`
	Company string `json:"company"`
}

// SyncUserRequest is used by the Clerk webhook to create or update a local user record.
type SyncUserRequest struct {
	ClerkID string `json:"clerk_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}
