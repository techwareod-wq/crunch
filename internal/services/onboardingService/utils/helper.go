package utils

import (
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
)

func ParseBusinessContext(cleanedResponse string) (*models.BusinessContext, error) {
	var bc models.BusinessContext
	if err := json.Unmarshal([]byte(cleanedResponse), &bc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal business context: %w", err)
	}

	return &bc, nil
}

func ParseCompetitorInfo(cleanedResponse string) (*[]models.Competitor, error) {
	var wrapper struct {
		Competitors []models.Competitor `json:"competitors"`
	}
	if err := json.Unmarshal([]byte(cleanedResponse), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to unmarshal competitor info: %w", err)
	}

	return &wrapper.Competitors, nil
}
