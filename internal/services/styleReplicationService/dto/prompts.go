package dto

// ToneStructureSynthesisPrompt is the template DTO for the tone + structure +
// title-pattern synthesis LLM call. Titles ride as their own numbered block —
// title style is a distinct signal from body voice — and Articles is the
// concatenated per-URL article dumps.
type ToneStructureSynthesisPrompt struct {
	ArticleCount int
	Titles       string
	Articles     string
}

// ToneStructureSynthesisResponse is the JSON contract of the tone + structure
// + title-pattern synthesis call.
type ToneStructureSynthesisResponse struct {
	ToneProfile      string `json:"tone_profile"`
	StructurePattern string `json:"structure_pattern"`
	TitlePattern     string `json:"title_pattern"`
}

// ImageStyleSynthesisPrompt is the template DTO for one position bucket's
// image style vision call — the images themselves ride as message image
// blocks. ImageKind frames what the images are ("blog thumbnail / hero" vs
// "mid-article illustration") so each bucket's rules stay position-specific.
type ImageStyleSynthesisPrompt struct {
	ImageCount int
	ImageKind  string
}

// ImageStyleSynthesisResponse is the JSON contract of one image style call.
type ImageStyleSynthesisResponse struct {
	ImageStylePrompt string `json:"image_style_prompt"`
}
