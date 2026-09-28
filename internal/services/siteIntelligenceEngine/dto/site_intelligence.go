package dto

import (
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

type SiteIntelligence struct {
	ID              string    `json:"id"`
	WebsiteUrl      string    `json:"websiteUrl"`
	BusinessName    string    `json:"businessName,omitempty"`
	ProductType     string    `json:"productType,omitempty"`
	KeyFeatures     []string  `json:"keyFeatures,omitempty"`
	BusinessModel   string    `json:"businessModel,omitempty"`
	TargetGeography string    `json:"targetGeography,omitempty"`
	CompetitorUrls  []string  `json:"competitorUrls,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (s *SiteIntelligence) FromDbModel(m *models.WebEntity) error {
	s.ID = m.ID.Hex()
	s.WebsiteUrl = m.WebsiteUrl
	// s.CompetitorUrls = m.CompetitorUrls
	s.CreatedAt = m.CreatedAt
	s.UpdatedAt = m.UpdatedAt

	if m.BusinessContext != nil {
		bc := m.BusinessContext
		if bc.BusinessName != nil {
			s.BusinessName = *bc.BusinessName
		}
		if bc.ProductType != nil {
			s.ProductType = *bc.ProductType
		}
		if bc.KeyFeatures != nil {
			s.KeyFeatures = bc.KeyFeatures
		}
		if bc.BusinessModel != nil {
			s.BusinessModel = *bc.BusinessModel
		}
		if bc.TargetGeography != nil {
			s.TargetGeography = *bc.TargetGeography
		}
	}

	return nil
}
