package dto

// ArticleTypePrompt is the template DTO for the article-type LLM call. Each
// field is referenced as {{.FieldName}} in the corresponding .md template.
type ArticleTypePrompt struct {
	Keyword      string  `json:"keyword"`
	Intent       string  `json:"intent"`
	Funnel       string  `json:"funnel"`
	Volume       int     `json:"volume"`
	CPC          float64 `json:"cpc"`
	BusinessName string  `json:"business_name"`
	ProductType  string  `json:"product_type"`
}

type ArticleTypeResponse struct {
	ArticleType string `json:"article_type"`
	Reasoning   string `json:"reasoning"`
}

// TitleGenerationPrompt is the template DTO for the title-generation LLM call.
// ICPRole is supplied as a single comma-joined string so the template can
// drop it in unchanged.
type TitleGenerationPrompt struct {
	Keyword      string `json:"keyword"`
	ArticleType  string `json:"article_type"`
	ICPRole      string `json:"icp_role"`
	BusinessName string `json:"business_name"`
	Funnel       string `json:"funnel"`
	// ToneProfile is the learned style profile (style replication) — empty
	// renders nothing. Templates prefer TitlePattern; the tone block renders
	// only as a fallback for profiles learned before title analysis existed.
	ToneProfile string `json:"tone_profile"`
	// TitlePattern is the learned TITLE style (casing, shape, punctuation
	// habits) — derived from the publisher's existing titles specifically,
	// which is the proper context for a title call. Empty renders nothing.
	TitlePattern string `json:"title_pattern"`
}

type TitleGenerationResponse struct {
	Title string `json:"title"`
}

// TitleSuggestionsPrompt is the template DTO for the 5-title suggestions LLM
// call. Mirrors TitleGenerationPrompt — the same inputs drive both, but the
// suggestions template asks for a set of distinct options.
type TitleSuggestionsPrompt struct {
	Keyword      string `json:"keyword"`
	ArticleType  string `json:"article_type"`
	ICPRole      string `json:"icp_role"`
	BusinessName string `json:"business_name"`
	Funnel       string `json:"funnel"`
	// ToneProfile is the learned style profile (style replication) — empty
	// renders nothing. Templates prefer TitlePattern; the tone block renders
	// only as a fallback for profiles learned before title analysis existed.
	ToneProfile string `json:"tone_profile"`
	// TitlePattern is the learned TITLE style (casing, shape, punctuation
	// habits) — derived from the publisher's existing titles specifically,
	// which is the proper context for a title call. Empty renders nothing.
	TitlePattern string `json:"title_pattern"`
}

type TitleSuggestionsResponse struct {
	Titles []string `json:"titles"`
}

// RetitlePrompt is the template DTO for the retitle-on-type-change LLM call.
// It carries the current title so the prompt can choose to reword it (rather
// than always writing from scratch) and the new article type to target.
type RetitlePrompt struct {
	Keyword      string `json:"keyword"`
	CurrentTitle string `json:"current_title"`
	ArticleType  string `json:"article_type"`
	ICPRole      string `json:"icp_role"`
	BusinessName string `json:"business_name"`
	Funnel       string `json:"funnel"`
	// ToneProfile is the learned style profile (style replication) — empty
	// renders nothing. Templates prefer TitlePattern; the tone block renders
	// only as a fallback for profiles learned before title analysis existed.
	ToneProfile string `json:"tone_profile"`
	// TitlePattern is the learned TITLE style (casing, shape, punctuation
	// habits) — derived from the publisher's existing titles specifically,
	// which is the proper context for a title call. Empty renders nothing.
	TitlePattern string `json:"title_pattern"`
}

type RetitleResponse struct {
	Title     string `json:"title"`
	Reasoning string `json:"reasoning"`
}
