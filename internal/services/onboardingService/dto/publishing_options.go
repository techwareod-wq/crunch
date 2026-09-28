package dto

import "github.com/atharva-ng/crunch/internal/services/onboardingService/constants"

// PublishingOptionsResponse is the static catalog the publishing screen
// renders (destinations, modes, cadences). Sourced from constants/publishing.go
// — that file is the single source of truth.
type PublishingOptionsResponse struct {
	Platforms []constants.PlatformOption    `json:"platforms"`
	Modes     []constants.PublishModeOption `json:"modes"`
	Cadences  []constants.CadenceOption     `json:"cadences"`
}
