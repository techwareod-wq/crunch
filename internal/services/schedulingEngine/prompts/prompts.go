package prompts

import _ "embed"

//go:embed articleType.md
var ArticleType string

//go:embed titleGeneration.md
var TitleGeneration string

//go:embed titleSuggestions.md
var TitleSuggestions string

//go:embed retitleForType.md
var RetitleForType string
