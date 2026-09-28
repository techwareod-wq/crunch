package interfaces

type LlmUtils interface {
	ConstructPrompt(content string, dto any) (string, error)
	CleanLLMResponse(raw string) (string, error)
}
