package interfaces

import "context"

// Embedding input types: a stored document vs a search query (the model
// embeds them asymmetrically).
const (
	EmbedDocument = "document"
	EmbedQuery    = "query"
)

// Embedder turns texts into vectors (AI search similar matches, D-082).
// Returns one vector per text, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string, inputType string) ([][]float32, error)
	// Model names the embedding model (stored with each vector's hash so a
	// model change re-embeds).
	Model() string
}
