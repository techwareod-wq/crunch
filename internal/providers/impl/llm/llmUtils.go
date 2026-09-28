package llm

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

var _ interfaces.LlmUtils = (*llmUtils)(nil)

type llmUtils struct{}

func NewLlmUtils() interfaces.LlmUtils {
	return &llmUtils{}
}

func (l *llmUtils) ConstructPrompt(content string, dto any) (string, error) {
	tmpl, err := template.New("prompt").Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, dto); err != nil {
		return "", fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return buf.String(), nil
}

func (l *llmUtils) CleanLLMResponse(raw string) (string, error) {
	s := strings.TrimSpace(raw)

	// Remove ```json ... ``` or ``` ... ``` fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}

	return strings.TrimSpace(s), nil
}
