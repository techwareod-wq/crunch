package interfaces

import (
	"context"
	"errors"

	"github.com/atharva-ng/crunch/internal/dto"
)

// Errors an LlmService may wrap so callers can tell failures apart.
var (
	// ErrLLMRateLimited: the provider returned 429 (or is cooling down
	// after one).
	ErrLLMRateLimited = errors.New("llm rate limited")
	// ErrLLMUnavailable: a 5xx or transport failure.
	ErrLLMUnavailable = errors.New("llm unavailable")
	// ErrLLMMalformed: the model answered, but not in the requested shape.
	ErrLLMMalformed = errors.New("llm output malformed")
)

type LlmService interface {
	Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error)
}
