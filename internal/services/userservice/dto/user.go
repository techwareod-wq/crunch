package dto

import "github.com/atharva-ng/crunch/internal/models"

type UserResponse struct {
	ID      string `json:"id"`
	ClerkID string `json:"clerk_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"`
}

func (u *UserResponse) FromDbModel(m *models.User) error {
	u.ID = m.ID.Hex()
	u.ClerkID = m.ClerkID
	u.Email = m.Email
	u.Name = m.Name
	u.Role = m.Role
	return nil
}

// SyncUserRequest is used by the Clerk webhook to create or update a local user record.
type SyncUserRequest struct {
	ClerkID string `json:"clerk_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}
