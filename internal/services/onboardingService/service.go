package onboardingService

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
)

// Async onboarding analysis chain. OnboardUser dispatches the first step after
// creating the web entity; ProcessOnboardedUser dispatches the second on
// success. Handlers unmarshal a pipeline.StandardPayload carrying WebEntityID...
const (
	ProcessOnboardingWebEntity      pipeline.ProcessType = "ONBOARDING_PROCESS_WEBENTITY"
	ProcessOnboardingCompetitorInfo pipeline.ProcessType = "ONBOARDING_GET_COMPETITOR_INFO"
)

type OnboardingService interface {
	OnboardUser(ctx context.Context, req dto.OnboardRequest, userId string) (string, bool, error)
	ProcessOnboardedUser(ctx context.Context, userId string, webEntityId string) error
	GetCompetitorInfo(ctx context.Context, userId string, webEntityId string) error
	EditOnboardedUser(ctx context.Context, req models.WebEntity, userId string, webEntityId string) error
	PatchOnboardedUser(ctx context.Context, req dto.PatchWebEntityRequest, userId string) (int, bool, error)
	GetOnboardingSteps(ctx context.Context, userId string) (*dto.OnboardingStepsResponse, error)
	GetCurrentWebEntity(ctx context.Context, userId string) (*dto.WebEntity, error)
	GetPublishingOptions() *dto.PublishingOptionsResponse
}
