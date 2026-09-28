package dto

import (
	llmDto "github.com/atharva-ng/crunch/internal/dto"
)

type OnboardRequest struct {
	WebsiteURL string `json:"websiteUrl" validate:"required"`
	Country    string `json:"country" validate:"required"`
}

type OnboardResponse struct {
	WebEntityID string                `json:"webEntityId"`
	LLMResponse llmDto.PromptResponse `json:"llmResponse,omitempty"`
}
