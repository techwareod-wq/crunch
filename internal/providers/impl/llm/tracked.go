package llm

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/tokentracker"
)

type trackedLLM struct {
	inner   interfaces.LlmService
	tracker *tokentracker.Tracker
}

// NewTrackedLLM wraps an LlmService so that every successful Prompt call
// automatically reports input+output tokens to the tracker.
func NewTrackedLLM(inner interfaces.LlmService, tracker *tokentracker.Tracker) interfaces.LlmService {
	return &trackedLLM{inner: inner, tracker: tracker}
}

func (t *trackedLLM) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	resp, err := t.inner.Prompt(ctx, req)
	if err != nil {
		return nil, err
	}
	t.tracker.Add(resp.PromptTokens + resp.OutputTokens)
	return resp, nil
}
