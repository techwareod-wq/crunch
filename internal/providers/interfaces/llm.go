package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

type LlmService interface {
	Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error)
}
