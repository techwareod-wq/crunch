package dto

type KeywordSuggestions struct {
	WebEntityID string   `json:"webEntityId"`
	Keywords    []string `json:"keywords"`
}
