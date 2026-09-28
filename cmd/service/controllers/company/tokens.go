package company

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
)

// mintToken returns (raw, sha256hex). The raw token exists only in the link;
// only the hash is stored (same discipline as the comms bind invites).
func mintToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

// hashToken is sha256(raw) hex — the stored/compared form.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// renderLink substitutes {token} — plus any extra {key}→value pairs — into a
// URL template. Empty template = no link (fail closed — the caller surfaces
// the raw path instead). Extra values are query-escaped; the token is already
// URL-safe base64.
func renderLink(template, token string, extra ...string) string {
	if strings.TrimSpace(template) == "" {
		return ""
	}
	out := strings.ReplaceAll(template, "{token}", token)
	for i := 0; i+1 < len(extra); i += 2 {
		out = strings.ReplaceAll(out, "{"+extra[i]+"}", url.QueryEscape(extra[i+1]))
	}
	return out
}

// companyDTO is the wire shape of a company for this surface.
type companyDTO struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Domain         string `json:"domain,omitempty"`
	WebsiteURL     string `json:"websiteUrl,omitempty"`
	OwnerUserID    string `json:"ownerUserId"`
	SeatsPurchased int    `json:"seatsPurchased"`
	SeatsUsed      int    `json:"seatsUsed"`
}

func toCompanyDTO(c *models.Company) companyDTO {
	return companyDTO{
		ID:             c.ID.Hex(),
		Kind:           c.Kind,
		Name:           c.Name,
		Domain:         c.Domain,
		WebsiteURL:     c.WebsiteURL,
		OwnerUserID:    c.OwnerUserID.Hex(),
		SeatsPurchased: c.Billing.SeatsPurchased,
		SeatsUsed:      c.SeatsUsed,
	}
}

// memberDTO is one membership row for the member list.
type memberDTO struct {
	MembershipID string `json:"membershipId"`
	Email        string `json:"email"`
	UserID       string `json:"userId,omitempty"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	IsOwner      bool   `json:"isOwner"`
	InvitedAt    string `json:"invitedAt,omitempty"`
	ClaimedAt    string `json:"claimedAt,omitempty"`
}

func toMemberDTO(m *models.CompanyMembership, ownerUserID string) memberDTO {
	dto := memberDTO{
		MembershipID: m.ID.Hex(),
		Email:        m.Email,
		Role:         m.Role,
		Status:       m.Status,
	}
	if m.UserID != nil {
		dto.UserID = m.UserID.Hex()
		dto.IsOwner = dto.UserID == ownerUserID
	}
	if !m.InvitedAt.IsZero() {
		dto.InvitedAt = m.InvitedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if m.ClaimedAt != nil {
		dto.ClaimedAt = m.ClaimedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return dto
}
