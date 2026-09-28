package dto

type OnboardingPromptRequest struct {
	WebsiteContent string `json:"WebsiteContent" validate:"required"`
}

type GetCompititorsPromptRequest struct {
	BusinessName  string `json:"BusinessName" validate:"required"`
	ProductType   string `json:"ProductType" validate:"required"`
	UserDomain    string `json:"UserDomain" validate:"required"`
	SearchResults string `json:"SearchResults" validate:"required"`
	// CompetitorCount is how many competitors the prompt asks for — the
	// auto-discovery count (onboarding.discoverCompetitors), not the
	// onboarding.maxCompetitors cap on what a user may keep.
	CompetitorCount int `json:"CompetitorCount" validate:"required"`
}
