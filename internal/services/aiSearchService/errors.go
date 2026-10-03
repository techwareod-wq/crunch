package aiSearchService

import (
	"errors"
	"fmt"
)

// ValidationError is a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf formats a ValidationError.
func Invalidf(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

var (
	// ErrNoLLM: no Anthropic key configured (the basic search runs instead).
	ErrNoLLM = errors.New("llm not configured")
	// ErrToolCallTruncated: the model hit max tokens mid tool call.
	ErrToolCallTruncated = errors.New("tool call truncated")
	// ErrNoSetFilters: the model didn't call set_filters.
	ErrNoSetFilters = errors.New("no set_filters call")
	// ErrNoDispatcher: async dispatch isn't wired.
	ErrNoDispatcher = errors.New("dispatcher not wired")
	// ErrNoEmbedder: no embedding provider configured.
	ErrNoEmbedder = errors.New("no embedder configured")
)
